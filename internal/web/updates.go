package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"sap13ns/internal/update"
)

// Release notices. serve runs an update.Checker; administrators see a banner
// in the admin when a newer release exists, and the System page shows the
// last check. Installing stays an operator task (sudo sap13ns update), since
// the server must not be able to replace its own binary.

type updateNotice struct {
	Current string
	Latest  string
	URL     string // release notes on GitHub, "" if unknown
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
	return &updateNotice{Current: Version, Latest: strings.TrimPrefix(rel.Version, "v"), URL: rel.URL}
}

func (s *Server) updateRows() []sysRow {
	yes, no := true, false
	flag := func(b bool) *bool {
		if b {
			return &yes
		}
		return &no
	}
	signed := update.PublicKey != ""
	rows := []sysRow{{Label: s.t("Signed updates"), Value: map[bool]string{true: s.t("Yes"), false: s.t("No: this build cannot install updates")}[signed], OK: flag(signed)}}
	if s.updates == nil || s.updates.Disabled {
		return append(rows, sysRow{Label: s.t("Release checks"), Value: s.t("Off (%s=false)", update.EnvCheck)})
	}
	rel, newer, checked := s.updates.Latest()
	switch {
	case checked.IsZero():
		rows = append(rows, sysRow{Label: s.t("Latest release"), Value: s.t("Not checked yet")})
	case rel == nil:
		rows = append(rows, sysRow{Label: s.t("Latest release"), Value: s.t("None published yet")})
	default:
		rows = append(rows, sysRow{Label: s.t("Latest release"), Value: strings.TrimPrefix(rel.Version, "v"), OK: flag(!newer)})
	}
	if !checked.IsZero() {
		rows = append(rows, sysRow{Label: s.t("Last check"), Value: checked.Local().Format("2006-01-02 15:04")})
	}
	if err := s.updates.LastError(); err != nil && !errors.Is(err, update.ErrNoRelease) {
		rows = append(rows, sysRow{Label: s.t("Last error"), Value: truncateErr(err), OK: flag(false)})
	}
	return rows
}

func (s *Server) adminUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if s.updates == nil || s.updates.Disabled {
		s.redirect(w, r, "/admin/system", "error", s.t("Release checks are turned off (%s=false).", update.EnvCheck))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	err := s.updates.CheckNow(ctx)
	s.audit(r, uid(r), "update.checked", "", nil)
	switch {
	case errors.Is(err, update.ErrNoRelease):
		s.redirect(w, r, "/admin/system", "info", s.t("No release has been published yet."))
	case err != nil:
		s.redirect(w, r, "/admin/system", "error", s.t("The release check failed: %s", truncateErr(err)))
	case s.updateNotice() != nil:
		s.redirect(w, r, "/admin/system", "info", s.t("sap13ns %s is available.", s.updateNotice().Latest))
	default:
		s.redirect(w, r, "/admin/system", "success", s.t("This site runs the latest release."))
	}
}
