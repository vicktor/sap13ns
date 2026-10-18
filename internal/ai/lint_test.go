package ai

import (
	"strings"
	"testing"
)

func phrases(fs []Finding, rule string) []string {
	var out []string
	for _, f := range fs {
		if rule == "" || f.Rule == rule {
			out = append(out, f.Phrase)
		}
	}
	return out
}

func checkOffsets(t *testing.T, text string, fs []Finding) {
	t.Helper()
	for _, f := range fs {
		if f.Start < 0 || f.End > len(text) || f.Start >= f.End || text[f.Start:f.End] != f.Phrase || f.Suggestion == "" {
			t.Errorf("bad finding %+v", f)
		}
	}
}

func TestLintSpanish(t *testing.T) {
	text := "Ñandú. En el mundo actual, CABE DESTACAR que la guía es Revolucionaria. " +
		"Sumergete en este viaje y desbloquearás todo. Un viajero no cuenta. En resumen: ¡Vamos allá!"
	fs := Lint(text, "es")
	checkOffsets(t, text, fs)
	got := strings.Join(phrases(fs, RuleCliche), "|")
	want := "En el mundo actual|CABE DESTACAR|Revolucionaria|Sumergete|desbloquearás|En resumen|¡Vamos allá"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	// English phrases are not applied to Spanish text.
	if fs := Lint("Let's delve into it.", "es"); len(fs) != 0 {
		t.Errorf("en phrase matched in es: %+v", fs)
	}
}

func TestLintEnglish(t *testing.T) {
	text := "In today’s fast-paced world, we delve into a rich tapestry. It’s worth noting this is a Game-Changer. In conclusion, unlocking value."
	fs := Lint(text, "en-US")
	checkOffsets(t, text, fs)
	got := strings.Join(phrases(fs, RuleCliche), "|")
	want := "In today’s fast-paced world|delve|tapestry|It’s worth noting|Game-Changer|In conclusion|unlocking"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	// Whole words only: "delve" must not match inside "shelved"; "unlocked" as a prefix rule does match.
	if fs := Lint("The shelved plan.", "en"); len(fs) != 0 {
		t.Errorf("matched inside a word: %+v", fs)
	}
}

func TestLintNotJust(t *testing.T) {
	es := "No es solo una herramienta, es una filosofía. No se trata solamente de dinero sino de tiempo."
	fs := Lint(es, "es")
	checkOffsets(t, es, fs)
	if got := phrases(fs, RuleNotJust); len(got) != 2 || got[0] != "No es solo una herramienta, es" {
		t.Errorf("es: %q", got)
	}
	en := "It's not just a tool, it's a mindset. This isn't only about speed but about care."
	fs = Lint(en, "en")
	checkOffsets(t, en, fs)
	if got := phrases(fs, RuleNotJust); len(got) != 2 || got[0] != "not just a tool, it's" {
		t.Errorf("en: %q", got)
	}
	if got := phrases(Lint("No es una herramienta. Es solo un paso.", "es"), RuleNotJust); len(got) != 0 {
		t.Errorf("false positive: %q", got)
	}
}

func TestLintEmDash(t *testing.T) {
	dense := "El plan —dijo— era simple — y caro — muy caro. Nada más."
	if got := phrases(Lint(dense, "es"), RuleEmDash); len(got) != 4 {
		t.Errorf("dense: %d dashes flagged", len(got))
	}
	sparse := strings.Repeat("Una frase normal con varias palabras. ", 40) + "Un inciso — uno solo."
	if got := phrases(Lint(sparse, "es"), RuleEmDash); len(got) != 0 {
		t.Errorf("sparse: %d dashes flagged", len(got))
	}
	dialogue := "—Hola —dijo ella.\n—Adiós.\n—Vale.\n" + strings.Repeat("Texto corriente aquí. ", 30)
	if got := phrases(Lint(dialogue, "es"), RuleEmDash); len(got) != 0 {
		t.Errorf("dialogue dashes flagged: %d", len(got))
	}
}

func TestLintSkipsCodeAndURLs(t *testing.T) {
	text := "Usa `delve` y [ver](https://example.com/en-resumen).\n\n```\nen conclusión, delve\n```\nhttps://x.test/game-changer"
	if fs := Lint(text, ""); len(fs) != 0 {
		t.Errorf("findings in code or URLs: %+v", fs)
	}
	unclosed := "Texto.\n\n```\nen resumen"
	if fs := Lint(unclosed, "es"); len(fs) != 0 {
		t.Errorf("findings in an unclosed fence: %+v", fs)
	}
}

func TestLintHeuristics(t *testing.T) {
	text := "# Título\n\n¿Alguna vez te has preguntado por qué? Esto 🚀🔥 es así.\n\n" +
		"Un párrafo con **clave**, **esencial** y **potente** en negrita."
	fs := Lint(text, "es")
	checkOffsets(t, text, fs)
	if got := phrases(fs, RuleQuestion); len(got) != 1 || got[0] != "¿Alguna vez te has preguntado por qué?" {
		t.Errorf("question: %q", got)
	}
	if got := phrases(fs, RuleEmoji); len(got) != 1 || got[0] != "🚀🔥" {
		t.Errorf("emoji: %q", got)
	}
	if got := phrases(fs, RuleBold); len(got) != 3 {
		t.Errorf("bold: %q", got)
	}
	if got := phrases(Lint("Una afirmación. ¿Y luego?", "es"), RuleQuestion); len(got) != 0 {
		t.Errorf("question later in the text flagged: %q", got)
	}
	// Findings are sorted by offset.
	for i := 1; i < len(fs); i++ {
		if fs[i].Start < fs[i-1].Start {
			t.Fatal("findings not sorted")
		}
	}
}

func TestLintLists(t *testing.T) {
	count := func(groups []phraseGroup) int {
		n := 0
		seen := map[string]bool{}
		for _, g := range groups {
			for _, p := range g.phrases {
				if seen[p] {
					t.Errorf("duplicate phrase %q", p)
				}
				seen[p] = true
				n++
			}
		}
		return n
	}
	if n := count(esPhrases); n < 60 {
		t.Errorf("es list has %d phrases", n)
	}
	if n := count(enPhrases); n < 60 {
		t.Errorf("en list has %d phrases", n)
	}
	// Every phrase must match itself.
	for _, l := range []struct {
		lang   string
		groups []phraseGroup
	}{{"es", esPhrases}, {"en", enPhrases}} {
		for _, g := range l.groups {
			for _, p := range g.phrases {
				p = strings.TrimSuffix(p, "*")
				if len(Lint("x "+p+" x", l.lang)) == 0 {
					t.Errorf("%s phrase %q does not match itself", l.lang, p)
				}
			}
		}
	}
}

func FuzzLint(f *testing.F) {
	f.Add("En el mundo actual — sí — no es solo esto, es aquello 🚀", "es")
	f.Add("```\nx\n", "")
	f.Fuzz(func(t *testing.T, text, lang string) {
		for _, fd := range Lint(text, lang) {
			if fd.Start < 0 || fd.End > len(text) || fd.Start >= fd.End || text[fd.Start:fd.End] != fd.Phrase {
				t.Fatalf("bad finding %+v", fd)
			}
		}
	})
}
