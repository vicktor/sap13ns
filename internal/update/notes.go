package update

import (
	"fmt"
	"strings"
)

// Release notes come from CHANGELOG.md: one "## vX.Y.Z" section per
// release, newest first, with an optional date after the version
// ("## v1.4.0 — 2026-11-02"). The release workflow publishes, as the notes
// of a release, its section and the ones before it (Notes), so a server that
// skipped releases still learns what each one changed (Since).

// Change is one release's section of the notes.
type Change struct {
	Version string // "v1.4.0"
	Date    string // as written after the version, "" if none
	Body    string // Markdown, untrusted when it comes from GitHub
}

// Changes splits a changelog, or the notes of a release, into its sections.
// Text before the first section and headings that do not name a version
// stay in the section they are in.
func Changes(md string) []Change {
	var out []Change
	var body strings.Builder
	flush := func() {
		if len(out) > 0 {
			out[len(out)-1].Body = strings.TrimSpace(body.String())
		}
		body.Reset()
	}
	for line := range strings.Lines(md) {
		if v, date, ok := sectionHeading(line); ok {
			flush()
			out = append(out, Change{Version: v, Date: date})
			continue
		}
		body.WriteString(line)
	}
	flush()
	return out
}

// sectionHeading parses "## v1.4.0 — 2026-11-02" (the date and its dash are
// optional).
func sectionHeading(line string) (version, date string, ok bool) {
	rest, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "## ")
	if !ok {
		return "", "", false
	}
	v, date, _ := strings.Cut(strings.TrimSpace(rest), " ")
	if _, err := parseVersion(v); err != nil || !strings.HasPrefix(v, "v") {
		return "", "", false
	}
	date = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(date), "—–-"))
	return v, date, true
}

// Since returns the changes of the releases newer than current, newest
// first. A current version that is not a semantic version ("dev") gets none.
func Since(notes, current string) []Change {
	var out []Change
	for _, c := range Changes(notes) {
		if IsNewer(c.Version, current) {
			out = append(out, c)
		}
	}
	return out
}

// Notes returns the release notes of version from a changelog: its section
// and the older ones, newest first, up to maxNotes bytes (whole sections
// only). It fails when the changelog has no section for version, so that a
// release cannot go out without one.
func Notes(changelog, version string) (string, error) {
	var b strings.Builder
	found := false
	for _, c := range Changes(changelog) {
		if !found {
			if c.Version != version {
				continue
			}
			found = true
		}
		head := "## " + c.Version
		if c.Date != "" {
			head += " — " + c.Date
		}
		section := head + "\n\n" + c.Body + "\n\n"
		if b.Len()+len(section) > maxNotes && b.Len() > 0 {
			break
		}
		b.WriteString(section)
	}
	if !found {
		return "", fmt.Errorf("the changelog has no section for %s (add \"## %s — YYYY-MM-DD\" to CHANGELOG.md)", version, version)
	}
	return strings.TrimSpace(b.String()) + "\n", nil
}
