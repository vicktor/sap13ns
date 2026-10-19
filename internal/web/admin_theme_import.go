package web

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"sap13ns/internal/archive"
	"sap13ns/internal/theme"
	"sap13ns/internal/themeimport"
)

// Theme import (docs/THEME-IMPORT.md). An upload that is a WordPress
// block theme is converted (internal/themeimport) instead of installed:
// the themes page shows what the conversion made and left out, and the
// administrator installs the result, or not. The converted archive waits
// in memory, bound to the user who uploaded it; installing it goes
// through the same checks and quota as any upload.

const (
	convertedTTL = 30 * time.Minute
	maxConverted = 2 // per site
)

type convertedTheme struct {
	token   string
	user    int64
	file    string
	pkg     []byte
	report  *themeimport.Report
	created time.Time
}

// convertedThemes are the conversions awaiting confirmation.
type convertedThemes struct {
	mu    sync.Mutex
	items []*convertedTheme
}

func (c *convertedThemes) prune(now time.Time) {
	c.items = slices.DeleteFunc(c.items, func(t *convertedTheme) bool { return now.Sub(t.created) > convertedTTL })
}

// put keeps a conversion, dropping the oldest beyond maxConverted.
func (c *convertedThemes) put(t *convertedTheme) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prune(time.Now())
	c.items = append(c.items, t)
	if len(c.items) > maxConverted {
		c.items = c.items[len(c.items)-maxConverted:]
	}
}

// take removes and returns the user's conversion of token.
func (c *convertedThemes) take(token string, user int64) *convertedTheme {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prune(time.Now())
	for i, t := range c.items {
		if t.token == token && t.user == user {
			c.items = slices.Delete(c.items, i, i+1)
			return t
		}
	}
	return nil
}

// themeImportView is a conversion, for the themes page.
type themeImportView struct {
	Token, File                   string
	Name, Slug, Version, Author   string
	License                       string
	Pages                         []string
	Blocks, Kinds, Images, Fonts  int
	Variations                    int
	Notes                         []themeImportNote
	Replaces                      bool // a theme of the slug is installed
	ReplacesOther, ReplacesActive bool
}

type themeImportNote struct {
	Where, Message string
	Count          int
}

// convertUpload converts an uploaded WordPress block theme and renders
// the page that confirms its installation.
func (s *Server) convertUpload(w http.ResponseWriter, r *http.Request, file io.ReaderAt, size int64, name string) {
	res, err := themeimport.Convert(file, size, themeimport.Options{})
	if err != nil {
		var ae *archive.Error
		msg := err.Error()
		if errors.As(err, &ae) {
			msg = ae.Msg
		}
		s.audit(r, uid(r), "theme.import-failed", "", map[string]string{"file": name, "error": truncateStr(msg, 200)})
		s.redirect(w, r, "/admin/appearance/themes", "error", s.tu(r, "The WordPress theme could not be converted: %s", msg))
		return
	}
	pkg, err := res.Zip()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if int64(len(pkg)) > theme.MaxArchiveBytes {
		s.redirect(w, r, "/admin/appearance/themes", "error", s.tu(r, "The converted theme is larger than %d MB.", theme.MaxArchiveBytes>>20))
		return
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	ct := &convertedTheme{token: hex.EncodeToString(b), user: uid(r), file: name, pkg: pkg, report: res.Report, created: time.Now()}
	s.converted.put(ct)
	s.audit(r, uid(r), "theme.converted", res.Slug, map[string]string{"file": name, "source": res.Report.Source,
		"version": res.Report.Version, "notes": strconv.Itoa(len(res.Report.Notes))})
	d, err := s.themesPage(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d.Import = s.importView(r, ct)
	s.renderAdmin(w, r, "themes", s.tu(r, "Themes"), "themes", d)
}

func (s *Server) importView(r *http.Request, ct *convertedTheme) *themeImportView {
	rep := ct.report
	v := &themeImportView{Token: ct.token, File: ct.file, Name: rep.Name, Slug: rep.Slug, Version: rep.Version,
		Author: rep.Author, License: rep.License, Pages: rep.Pages, Kinds: len(rep.Blocks), Images: rep.Images,
		Fonts: rep.Fonts, Variations: rep.Variations}
	for _, n := range rep.Blocks {
		v.Blocks += n
	}
	if th, ok := s.themes.Get(rep.Slug); ok {
		v.Replaces = true
		v.ReplacesOther = !th.Own
		v.ReplacesActive = th.Slug == s.settings().ActiveTheme
	}
	notes := slices.Clone(rep.Notes)
	sort.SliceStable(notes, func(i, j int) bool { return notes[i].File == "" && notes[j].File != "" })
	for _, n := range notes {
		v.Notes = append(v.Notes, themeImportNote{Where: n.File, Message: s.tu(r, themeimport.Messages[n.Code], n.Args...), Count: n.Count})
	}
	return v
}

// adminThemeImport installs a converted theme, or drops it.
func (s *Server) adminThemeImport(w http.ResponseWriter, r *http.Request) {
	const back = "/admin/appearance/themes"
	ct := s.converted.take(strings.TrimSpace(r.FormValue("token")), uid(r))
	if r.FormValue("action") == "cancel" {
		s.redirect(w, r, back, "success", s.tu(r, "The converted theme was not installed."))
		return
	}
	if ct == nil {
		s.redirect(w, r, back, "error", s.tu(r, "The converted theme is no longer waiting: upload the WordPress theme again."))
		return
	}
	pol, err := s.themePolicy(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !pol.Uploads {
		s.adminError(w, r, http.StatusForbidden, s.tu(r, "On the %s plan, sites use the built-in themes and the network's. Uploading your own needs a paid plan.", pol.Plan))
		return
	}
	s.installTheme(w, r, bytes.NewReader(ct.pkg), int64(len(ct.pkg)), ct.file, ct.report.Source)
}

// importRefusal is the message for an upload of a theme sap13ns cannot
// convert; "" for one it installs or converts.
func (s *Server) importRefusal(r *http.Request, k themeimport.Kind) string {
	switch k {
	case themeimport.KindWordPressClassic:
		return s.tu(r, "This is a classic WordPress theme (PHP templates). sap13ns converts WordPress block themes only: look for a block version of the theme.")
	case themeimport.KindGhost:
		return s.tu(r, "This is a Ghost theme. sap13ns cannot convert Ghost themes yet; it converts WordPress block themes.")
	case themeimport.KindHugo:
		return s.tu(r, "This is a Hugo theme. sap13ns cannot convert Hugo themes yet; it converts WordPress block themes.")
	case themeimport.KindJekyll:
		return s.tu(r, "This is a Jekyll theme. sap13ns cannot convert Jekyll themes; it converts WordPress block themes.")
	}
	return ""
}
