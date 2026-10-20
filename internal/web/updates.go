package web

import (
	"context"
	"errors"
	"html/template"
	"net/http"
	"strings"
	"time"

	"sap13ns/internal/content"
	"sap13ns/internal/update"
)

// Release notices. serve runs an update.Checker; administrators see a banner
// in the admin when a newer release exists, and the System page shows the
// last check. Installing stays an operator task (sudo sap13ns update), since
// the server must not be able to replace its own binary.

type updateNotice struct {
	Current string
	Latest  string
	URL     string          // release notes on GitHub, "" if unknown
	Changes []releaseChange // set by the System page

	notes string
}

// releaseChange is one newer release's section of the notes, rendered
// through the content pipeline (the notes come from GitHub).
type releaseChange struct {
	Version string
	Date    string
	HTML    template.HTML
}

// SetUpdateChecker attaches the release checker started by serve. Call it
// before the server handles requests.
func (s *Server) SetUpdateChecker(c *update.Checker) { s.updates = c }

// updateNotice returns the banner for a newer release, or nil.
func (s *Server) updateNotice() *updateNotice {
	if s.updates == nil {
		return nil
	}
	rel, newer, _ := s.updates.Latest()
	if rel == nil || !newer {
		return nil
	}
	return &updateNotice{Current: Version, Latest: strings.TrimPrefix(rel.Version, "v"), URL: rel.URL, notes: rel.Notes}
}

// changes renders the sections of the release notes newer than the running
// version, for the System page.
func (n *updateNotice) changes() []releaseChange {
	var out []releaseChange
	for _, c := range update.Since(n.notes, Version) {
		out = append(out, releaseChange{Version: strings.TrimPrefix(c.Version, "v"), Date: c.Date, HTML: content.RenderPlain(c.Body)})
	}
	return out
}

func (s *Server) updateRows(r *http.Request) []sysRow {
	yes, no := true, false
	flag := func(b bool) *bool {
		if b {
			return &yes
		}
		return &no
	}
	signed := update.PublicKey != ""
	rows := []sysRow{{Label: s.tu(r, "Signed updates"), Value: map[bool]string{true: s.tu(r, "Yes"), false: s.tu(r, "No: this build cannot install updates")}[signed], OK: flag(signed)}}
	if s.updates == nil || s.updates.Disabled {
		return append(rows, sysRow{Label: s.tu(r, "Release checks"), Value: s.tu(r, "Off (%s=false)", update.EnvCheck)})
	}
	rel, newer, checked := s.updates.Latest()
	switch {
	case checked.IsZero():
		rows = append(rows, sysRow{Label: s.tu(r, "Latest release"), Value: s.tu(r, "Not checked yet")})
	case rel == nil:
		rows = append(rows, sysRow{Label: s.tu(r, "Latest release"), Value: s.tu(r, "None published yet")})
	default:
		rows = append(rows, sysRow{Label: s.tu(r, "Latest release"), Value: strings.TrimPrefix(rel.Version, "v"), OK: flag(!newer)})
	}
	if !checked.IsZero() {
		rows = append(rows, sysRow{Label: s.tu(r, "Last check"), Value: checked.Local().Format("2006-01-02 15:04")})
	}
	if err := s.updates.LastError(); err != nil && !errors.Is(err, update.ErrNoRelease) {
		rows = append(rows, sysRow{Label: s.tu(r, "Last error"), Value: truncateErr(err), OK: flag(false)})
	}
	return rows
}

func (s *Server) adminUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if s.updates == nil || s.updates.Disabled {
		s.redirect(w, r, "/admin/system", "error", s.tu(r, "Release checks are turned off (%s=false).", update.EnvCheck))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	err := s.updates.CheckNow(ctx)
	s.audit(r, uid(r), "update.checked", "", nil)
	switch {
	case errors.Is(err, update.ErrNoRelease):
		s.redirect(w, r, "/admin/system", "info", s.tu(r, "No release has been published yet."))
	case err != nil:
		s.redirect(w, r, "/admin/system", "error", s.tu(r, "The release check failed: %s", truncateErr(err)))
	case s.updateNotice() != nil:
		s.redirect(w, r, "/admin/system", "info", s.tu(r, "sap13ns %s is available.", s.updateNotice().Latest))
	default:
		s.redirect(w, r, "/admin/system", "success", s.tu(r, "This site runs the latest release."))
	}
}
