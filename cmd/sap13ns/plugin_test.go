package main

import (
	"bytes"
	"strings"
	"testing"

	"sap13ns/internal/plugin"
)

func TestTermSafe(t *testing.T) {
	for in, want := range map[string]string{
		"plain text, ñ and 🙂":   "plain text, ñ and 🙂",
		"a\x1b[2Jb":             `a\u001b[2Jb`,
		"line\rover":            `line\u000dover`,
		"x\u202egnp.exe":        `x\u202egnp.exe`,
		"\u009b31m":             `\u009b31m`,
		"\x9b31m":               `\ufffd31m`,
		"bad \xff byte":         `bad \ufffd byte`,
		"sep\u2028arator\u2066": `sep\u2028arator\u2066`,
	} {
		if got := termSafe(in); got != want {
			t.Errorf("termSafe(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrintReportEscapes(t *testing.T) {
	var b bytes.Buffer
	printReport(&b, &plugin.Report{PluginID: "x", Version: "1.0.0\x1b]0;owned\x07", Verdict: plugin.VerdictBlocked,
		Findings: []plugin.Finding{{Severity: plugin.SevCritical, Rule: "package.path", Message: "bad entry", Location: "a\x1b[1A\rb"}}})
	if out := b.String(); strings.ContainsAny(out, "\x1b\x07\r") {
		t.Fatalf("control characters reached the terminal: %q", out)
	}
}
