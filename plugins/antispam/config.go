// The antispam plugin classifies new comments before they are stored.
//
// comment.check runs the local checks of checks.go (links, blocklist,
// alphabets, capitals, repetitions, first-time commenters with links) and,
// when enabled and the comment looks clean, asks StopForumSpam (sfs.go).
// comment.approved teaches the plugin the names of approved commenters
// (known.go). The strictest signal decides the verdict, and the reason
// lists every signal.
//
// Everything except main.go is plain Go, tested natively with sdktest.
package main

import (
	"strconv"
	"strings"
)

// Config holds the plugin settings, normalized.
type Config struct {
	MaxLinks  int // more links than this: pending
	SpamLinks int // at least this many: spam (0: off)

	Blocklist   []Entry
	BlockAction string // spam | pending

	Script         string // off | mixed | latin
	Caps           bool
	Repeat         bool
	FirstTimeLinks bool
	LogOK          bool

	SFS          bool
	SFSIP        bool
	SFSEmail     bool
	SFSThreshold float64

	Warnings []string
}

// Settings defaults (keep in sync with plugin.json).
const (
	defaultMaxLinks     = 2
	defaultSpamLinks    = 6
	defaultSFSThreshold = 60

	maxEntries  = 1000
	maxEntryLen = 100
	verdictOK   = "ok"
	verdictHold = "pending"
	verdictSpam = "spam"
	scriptOff   = "off"
	scriptMixed = "mixed"
	scriptLatin = "latin"
)

// ConfigFrom reads the plugin settings.
func ConfigFrom(s map[string]string) Config {
	get := func(k string) string { return strings.TrimSpace(s[k]) }
	flag := func(k string, def bool) bool {
		switch get(k) {
		case "true":
			return true
		case "false":
			return false
		}
		return def
	}
	c := Config{
		MaxLinks:       defaultMaxLinks,
		SpamLinks:      defaultSpamLinks,
		BlockAction:    verdictSpam,
		Script:         scriptOff,
		Caps:           flag("caps_check", true),
		Repeat:         flag("repeat_check", true),
		FirstTimeLinks: flag("first_time_links", true),
		LogOK:          flag("log_ok", true),
		SFS:            flag("sfs_enabled", false),
		SFSIP:          flag("sfs_check_ip", true),
		SFSEmail:       flag("sfs_check_email", true),
		SFSThreshold:   defaultSFSThreshold,
	}
	if n, err := strconv.Atoi(get("max_links")); err == nil && n >= 0 {
		c.MaxLinks = n
	} else if get("max_links") != "" {
		c.Warnings = append(c.Warnings, "“Links before holding” must be a whole number from 0; using 2")
	}
	if n, err := strconv.Atoi(get("spam_links")); err == nil && n >= 0 {
		c.SpamLinks = n
	} else if get("spam_links") != "" {
		c.Warnings = append(c.Warnings, "“Links for spam” must be a whole number from 0; using 6")
	}
	if get("blocklist_action") == verdictHold {
		c.BlockAction = verdictHold
	}
	switch v := get("script_check"); v {
	case scriptMixed, scriptLatin:
		c.Script = v
	}
	if f, err := strconv.ParseFloat(get("sfs_threshold"), 64); err == nil && f >= 1 && f <= 100 {
		c.SFSThreshold = f
	} else if get("sfs_threshold") != "" {
		c.Warnings = append(c.Warnings, "“StopForumSpam: spam confidence” must be a number from 1 to 100; using 60")
	}
	var dropped int
	c.Blocklist, dropped = ParseBlocklist(s["blocklist"])
	if dropped > 0 {
		c.Warnings = append(c.Warnings, "the blocklist has more than "+strconv.Itoa(maxEntries)+" entries; "+strconv.Itoa(dropped)+" were ignored")
	}
	return c
}

// Entry is one blocklist line, folded (see Fold).
type Entry struct {
	Text   string // as written, for the reason
	Folded string
	Phrase bool // contains a space: matched as a substring
}

// ParseBlocklist reads one entry per line, skipping blank lines and notes
// (# …). It returns the entries and how many were over the limit.
func ParseBlocklist(s string) ([]Entry, int) {
	var out []Entry
	seen := map[string]bool{}
	dropped := 0
	for _, line := range strings.Split(s, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if r := []rune(line); len(r) > maxEntryLen {
			line = string(r[:maxEntryLen])
		}
		f := Fold(line)
		if f == "" || seen[f] {
			continue
		}
		if len(out) == maxEntries {
			dropped++
			continue
		}
		seen[f] = true
		out = append(out, Entry{Text: line, Folded: f, Phrase: strings.Contains(f, " ")})
	}
	return out, dropped
}
