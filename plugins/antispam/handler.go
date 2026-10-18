package main

import (
	"fmt"
	"strings"

	"github.com/vicktor/sap13ns/pluginsdk"
)

// handle answers comment.check. It never returns an error: the host holds
// a comment whose check failed, so it always answers with an explicit
// verdict, "ok" included. A problem (StopForumSpam unavailable) is
// explained in the log while the local checks still decide.
func handle(req *pluginsdk.Request) (any, error) {
	cfg := ConfigFrom(req.Plugin.Settings)
	for _, w := range cfg.Warnings {
		pluginsdk.Log(pluginsdk.Warn, w)
	}
	switch {
	case req.Hook == pluginsdk.HookCommentCheck:
		var c pluginsdk.CommentCheck
		if err := req.Decode(&c); err != nil {
			pluginsdk.Log(pluginsdk.Warn, "comment.check: unreadable payload: "+err.Error())
			return pluginsdk.Verdict(verdictOK, ""), nil
		}
		v, reason := check(cfg, c)
		return pluginsdk.Verdict(v, reason), nil
	}
	return nil, nil
}

// check classifies a new comment. The host logs held and spam comments
// itself ("comment.check: pending by …"); accepted ones are logged here
// when log_ok is on.
//
// A known commenter is one whose e-mail address had a comment approved
// before, which the host checks (c.Returning). A name is not enough: anyone
// can type a regular's name.
func check(cfg Config, c pluginsdk.CommentCheck) (string, string) {
	known := c.Returning
	signals := Check(Comment{Author: c.Author, URL: c.URL, Content: c.Content, Email: c.Email, Registered: c.Registered, Known: known}, cfg)
	verdict, reason := Decide(signals)

	// StopForumSpam only for comments that look clean: a slow answer can
	// use up the host's two-second budget, and the host then discards the
	// whole answer and holds the comment, so a local "spam" is never
	// downgraded to "pending".
	var notes []string
	if cfg.SFS && verdict == verdictOK {
		if _, ok := SFSQuery(cfg, c.IP, c.Email); !ok {
			notes = append(notes, "StopForumSpam not asked: no public IP address or e-mail address to look up")
		} else {
			switch s, err := AskSFS(cfg, c.IP, c.Email); {
			case err != nil:
				pluginsdk.Log(pluginsdk.Warn, fmt.Sprintf("comment.check by %q: %v; judged by the local checks only", short(c.Author), err))
				notes = append(notes, "StopForumSpam not available")
			case s != nil:
				verdict, reason = Decide([]Signal{*s})
			default:
				notes = append(notes, "not listed by StopForumSpam")
			}
		}
	}
	if verdict == verdictOK && cfg.LogOK {
		pluginsdk.Log(pluginsdk.Info, fmt.Sprintf("comment.check: ok by %q: %s", short(c.Author), okReason(c, known, notes)))
	}
	return verdict, reason
}

// okReason says why a comment passed, for the log.
func okReason(c pluginsdk.CommentCheck, known bool, notes []string) string {
	parts := []string{"no spam signals"}
	switch links := len(linkRe.FindAllStringIndex(c.Content, -1)); {
	case links == 1:
		parts = append(parts, "1 link")
	case links > 1:
		parts = append(parts, fmt.Sprintf("%d links", links))
	}
	switch {
	case c.Registered:
		parts = append(parts, "signed-in user")
	case known:
		parts = append(parts, "known commenter")
	default:
		parts = append(parts, "first comment")
	}
	return strings.Join(append(parts, notes...), "; ")
}

// short truncates a name for the log, as the host does (60 runes).
func short(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return s
}
