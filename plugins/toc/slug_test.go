package main

import (
	"regexp"
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Introducción", "introduccion"},
		{"¿Qué es sap13ns?", "que-es-sap13ns"},
		{"Ça va? Très bien!", "ca-va-tres-bien"},
		{"Straße und Größe", "strasse-und-grosse"},
		{"Ærøskøbing", "aeroskobing"},
		{"Łódź", "lodz"},
		{"Țară și Șes", "tara-si-ses"},
		{"Tiếng Việt", "tieng-viet"},
		{"Привет, мир", "privet-mir"},
		{"Йошкар-Ола и Ёлка", "yoshkar-ola-i-yolka"},
		{"Їжак і єнот", "yizhak-i-yenot"},
		{"Ελληνικά", "ellinika"},
		{"Άλφα Ωμέγα", "alfa-omega"},
		{"  --Hello__World--  ", "hello-world"},
		{"C++ & Go", "c-go"},
		{"Don't panic", "dont-panic"},
		{"Version 2.0 (beta)", "version-2-0-beta"},
		{"ﬁle №１", "file-no1"},
		{"日本語 1", "1"},
	} {
		if got := slugify(tc.in); got != tc.want {
			t.Errorf("slugify(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSlugifyFallback(t *testing.T) {
	hashed := regexp.MustCompile(`^section-[0-9a-f]{8}$`)
	for _, in := range []string{"日本語", "مرحبا", "🚀🚀", "", "  ", "¿?"} {
		got := slugify(in)
		if !hashed.MatchString(got) {
			t.Errorf("slugify(%q) = %q, want a section-<hash> id", in, got)
		}
		if again := slugify(in); again != got {
			t.Errorf("slugify(%q) is not stable: %q then %q", in, got, again)
		}
	}
	if slugify("日本語") == slugify("中文") {
		t.Error("different texts share a fallback id")
	}
}

func TestSlugifyLength(t *testing.T) {
	got := slugify(strings.Repeat("palabra ", 40))
	if len(got) > maxSlugLen || strings.HasSuffix(got, "-") || got == "" {
		t.Errorf("long slug = %q (%d bytes)", got, len(got))
	}
	// ASCII-only ids pass the host sanitizer's id rules.
	valid := regexp.MustCompile(`^[a-z0-9-]{1,80}$`)
	for _, in := range []string{"Ünïcödé ½ ™", "αβγ – δεζ", "ŋ ħ ŧ"} {
		if got := slugify(in); !valid.MatchString(got) {
			t.Errorf("slugify(%q) = %q is not a plain ASCII id", in, got)
		}
	}
}

func TestUniqueID(t *testing.T) {
	used := map[string]bool{"intro": true}
	for _, want := range []string{"intro-2", "intro-3"} {
		if got := uniqueID("intro", used); got != want {
			t.Errorf("uniqueID = %q, want %q", got, want)
		}
	}
	if got := uniqueID("other", used); got != "other" {
		t.Errorf("uniqueID(other) = %q", got)
	}
	used["x-2"] = true
	uniqueID("x", used)
	if got := uniqueID("x", used); got != "x-3" {
		t.Errorf("uniqueID skips taken suffixes: got %q, want x-3", got)
	}
}
