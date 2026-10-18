package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/tetratelabs/wazero/api"
)

// ------------------------------------------------------------- secrets --

// Secret settings are stored as secretPrefix + base64(AES-256-GCM), bound to
// the plugin and the setting key so a value cannot be moved elsewhere.
const secretPrefix = "enc1:"

func secretAD(pluginID, key string) []byte {
	return []byte("plugin-secret\x00" + pluginID + "\x00" + key)
}

func (h *Host) sealSecret(pluginID, key, value string) (string, error) {
	if h.secrets == nil {
		return value, nil // tests and tools without instance keys
	}
	enc, err := h.secrets.Encrypt([]byte(value), secretAD(pluginID, key))
	if err != nil {
		return "", err
	}
	return secretPrefix + base64.RawStdEncoding.EncodeToString(enc), nil
}

func (h *Host) openSecret(pluginID, key, stored string) (string, error) {
	raw, ok := strings.CutPrefix(stored, secretPrefix)
	if !ok {
		return stored, nil // saved before encryption existed
	}
	if h.secrets == nil {
		return "", errors.New("secret settings need the instance keys")
	}
	enc, err := base64.RawStdEncoding.DecodeString(raw)
	if err != nil {
		return "", err
	}
	b, err := h.secrets.Decrypt(enc, secretAD(pluginID, key))
	return string(b), err
}

// pluginSettings are the values sent to the plugin: defaults merged with
// the stored values, secrets decrypted.
func (h *Host) pluginSettings(p *Plugin) map[string]string {
	vals := p.SettingValues()
	for _, s := range p.Settings {
		if s.Type != "secret" || vals[s.Key] == "" {
			continue
		}
		v, err := h.openSecret(p.ID, s.Key, vals[s.Key])
		if err != nil {
			p.addLog("error", fmt.Sprintf("setting %s: cannot decrypt the stored secret; save it again", s.Key))
			v = ""
		}
		vals[s.Key] = v
	}
	return h.exportTimes(p.Settings, vals)
}

// exportTimes rewrites datetime values, stored as local site time, as RFC
// 3339 with the right offset, so plugins need no time zone database.
func (h *Host) exportTimes(decls []SettingDecl, vals map[string]string) map[string]string {
	var loc *time.Location
	for _, s := range decls {
		if s.Type != "datetime" || vals[s.Key] == "" {
			continue
		}
		if loc == nil {
			loc = h.siteLocation()
		}
		if t, err := ParseSettingTime(vals[s.Key], loc); err == nil {
			vals[s.Key] = t.Format(time.RFC3339)
		} else {
			vals[s.Key] = ""
		}
	}
	return vals
}

func validEmail(s string) bool {
	if len(s) > 254 || strings.ContainsAny(s, "\r\n,;<>") {
		return false
	}
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s
}

// ---------------------------------------------------------------- mail --

// MailPerHour is the default number of e-mails each plugin may send per hour.
const MailPerHour = 20

const (
	maxMailPerCall = 5
	maxMailSubject = 200
	maxMailBody    = 64 << 10
)

type mailJob struct {
	plugin  *Plugin
	to      string
	replyTo string
	subject string
	body    string
}

type mailRequest struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	ReplyTo string `json:"reply_to"`
}

// mailRecipients are the addresses an administrator saved in the plugin's
// settings of type "email": the only ones besides the site administrator a
// plugin may write to.
func mailRecipients(p *Plugin) []string {
	var out []string
	vals := p.SettingValues()
	for _, s := range p.Settings {
		if v := strings.TrimSpace(vals[s.Key]); s.Type == "email" && v != "" && validEmail(v) {
			out = append(out, strings.ToLower(v))
		}
	}
	return out
}

// mail_send(ptr, len) -> 0 | code. The message is queued: delivery happens
// in the background so that a slow SMTP server never stalls a hook.
func (h *Host) hostMailSend(ctx context.Context, m api.Module, ptr, size uint32) int32 {
	st := state(ctx)
	if st == nil {
		return codeInvalid
	}
	if st.dryRun || !st.allowMail || !st.granted(PermMail) || h.mailer == nil || h.store == nil {
		return codeDenied
	}
	st.mailCalls++
	if st.mailCalls > maxMailPerCall {
		return codeLimit
	}
	raw, ok := readMem(m, ptr, size, maxMailBody+8<<10)
	if !ok {
		return codeInvalid
	}
	var req mailRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return codeInvalid
	}
	req.Subject = strings.TrimSpace(req.Subject)
	if req.Subject == "" || len(req.Subject) > maxMailSubject || strings.ContainsAny(req.Subject, "\r\n") || len(req.Body) > maxMailBody {
		return codeInvalid
	}
	if req.ReplyTo != "" && !validEmail(req.ReplyTo) {
		return codeInvalid
	}
	to := strings.ToLower(strings.TrimSpace(req.To))
	switch {
	case to == "admin":
		addr, err := h.store.SiteAdminEmail(ctx)
		if err != nil || !validEmail(addr) {
			return codeFailed
		}
		to = addr
	case !validEmail(to) || !containsFold(mailRecipients(st.plugin), to):
		st.plugin.addLog("warn", "mail_send: refused recipient "+truncate(to, 80)+" (only the administrator or addresses saved in email settings)")
		return codeDenied
	}
	if !h.mailLimit.Allow(st.plugin.ID) {
		st.plugin.addLog("warn", fmt.Sprintf("mail_send: hourly limit of %d e-mails reached", h.mailPerHour))
		return codeRateLimited
	}
	select {
	case h.mailq <- mailJob{plugin: st.plugin, to: to, replyTo: req.ReplyTo, subject: req.Subject, body: strings.ToValidUTF8(req.Body, "?")}:
		return 0
	default:
		return codeLimit
	}
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

func (h *Host) mailWorker() {
	defer h.wg.Done()
	for job := range h.mailq {
		site := h.site()
		body := job.body + "\n\n-- \n" + fmt.Sprintf("Sent by the plugin %q (%s) of %s", job.plugin.Name, job.plugin.ID, site.URL)
		if err := h.mailer.SendMessage(job.to, job.replyTo, job.subject, body); err != nil {
			job.plugin.addLog("error", "mail_send: "+err.Error())
			continue
		}
		job.plugin.addLog("info", "mail_send: e-mail sent to "+job.to)
	}
}
