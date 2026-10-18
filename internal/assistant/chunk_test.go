package assistant

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunkSmallPostIsOnePiece(t *testing.T) {
	md := "Intro text.\n\n## Prices\n\nTen euros.\n\n## Hours\n\nOpen *daily*.\n"
	got := Chunk("Our shop", "", md, DefaultChunking)
	if len(got) != 1 {
		t.Fatalf("%d pieces, want 1: %+v", len(got), got)
	}
	if got[0].Heading != "Our shop" {
		t.Errorf("heading %q", got[0].Heading)
	}
	for _, want := range []string{"Intro text.", "## Prices", "Ten euros.", "## Hours"} {
		if !strings.Contains(got[0].Content, want) {
			t.Errorf("content lacks %q:\n%s", want, got[0].Content)
		}
	}
	if !strings.HasPrefix(got[0].Text(), "Our shop\n\n") {
		t.Errorf("Text() = %q", got[0].Text())
	}
}

func TestChunkHeadingPathsAndCleaning(t *testing.T) {
	long := strings.Repeat("Lorem ipsum dolor sit amet, consectetur adipiscing elit. ", 40) // ~2,300 chars
	md := "# Guide\n\n" + long + "\n\n## Setup\n\n### Linux\n\n" + long +
		"\n\nSee ![the diagram](/u/d.png) and [the docs](https://example.com/docs \"t\").\n\n" +
		"<div class=\"x\">raw</div>\n\n[gallery ids=\"1,2\"]\n\n[ref]: https://example.com\n\n" +
		"```sh\n# not a heading\nmake install\n```\n\nSetext title\n------------\n\n" + long
	got := Chunk("Install", "", md, DefaultChunking)
	var headings []string
	for _, p := range got {
		headings = append(headings, p.Heading)
		if strings.Contains(p.Content, "](") || strings.Contains(p.Content, "<div") || strings.Contains(p.Content, "[gallery") ||
			strings.Contains(p.Content, "[ref]:") {
			t.Errorf("markup left in %q: %s", p.Heading, p.Content)
		}
	}
	want := []string{"Install › Guide", "Install › Guide › Setup › Linux", "Install › Guide › Setext title"}
	if strings.Join(headings, "|") != strings.Join(want, "|") {
		t.Errorf("headings = %q, want %q", headings, want)
	}
	if !strings.Contains(got[1].Content, "the diagram and the docs") || !strings.Contains(got[1].Content, "# not a heading") {
		t.Errorf("cleaned content: %s", got[1].Content)
	}
}

func TestChunkSplitsLongSectionsWithOverlap(t *testing.T) {
	var paras []string
	for i := 0; i < 40; i++ {
		paras = append(paras, strings.Repeat("palabra ", 30)+"fin del párrafo "+string(rune('A'+i%26))+".")
	}
	md := "## Historia\n\n" + strings.Join(paras, "\n\n")
	got := Chunk("Empresa", "", md, DefaultChunking)
	if len(got) < 4 {
		t.Fatalf("%d pieces for ~10,000 characters", len(got))
	}
	for i, p := range got {
		n := utf8.RuneCountInString(p.Content)
		if n > DefaultChunking.Max {
			t.Errorf("piece %d has %d characters", i, n)
		}
		if p.Heading != "Empresa › Historia" {
			t.Errorf("piece %d heading %q", i, p.Heading)
		}
		if i > 0 {
			// The previous piece's last paragraph opens this one.
			prev := got[i-1].Content
			last := prev[strings.LastIndex(prev, "\n\n")+2:]
			if !strings.HasPrefix(p.Content, last) {
				t.Errorf("piece %d does not overlap the previous one", i)
			}
		}
	}
}

func TestChunkHugeParagraph(t *testing.T) {
	md := strings.Repeat("Una frase corta sin más. ", 600) // one 15,000-character paragraph
	got := Chunk("T", "", md, DefaultChunking)
	if len(got) < 5 {
		t.Fatalf("%d pieces", len(got))
	}
	for _, p := range got {
		if n := utf8.RuneCountInString(p.Content); n > DefaultChunking.Max {
			t.Errorf("%d characters", n)
		}
		if !strings.HasSuffix(p.Content, ".") {
			t.Errorf("not cut at a sentence: …%s", p.Content[len(p.Content)-20:])
		}
	}
	words := strings.Repeat("x", 7000) // no space at all: hard cut
	if got := Chunk("T", "", words, DefaultChunking); len(got) != 3 {
		t.Errorf("%d pieces for an unbroken string", len(got))
	}
}

func TestChunkExcerptAndEmpty(t *testing.T) {
	got := Chunk("T", "A short summary.", "## A\n\nBody.", DefaultChunking)
	if len(got) != 1 || !strings.HasPrefix(got[0].Content, "A short summary.") {
		t.Errorf("excerpt not first: %+v", got)
	}
	got = Chunk("T", "Same start.", "Same start. And more.", DefaultChunking)
	if strings.Count(got[0].Content, "Same start.") != 1 {
		t.Errorf("excerpt repeated: %q", got[0].Content)
	}
	if got := Chunk("Empty", "", "  \n<!-- nothing -->\n", DefaultChunking); len(got) != 0 {
		t.Errorf("empty post gave %+v", got)
	}
}

func TestChunkFenceKeepsHashes(t *testing.T) {
	md := "~~~\n## inside\n~~~\n\nAfter."
	got := Chunk("T", "", md, DefaultChunking)
	if len(got) != 1 || got[0].Heading != "T" || !strings.Contains(got[0].Content, "## inside") {
		t.Errorf("fenced heading taken as a heading: %+v", got)
	}
}
