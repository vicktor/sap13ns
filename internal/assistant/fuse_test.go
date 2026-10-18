package assistant

import (
	"math"
	"testing"

	"sap13ns/internal/store"
)

func hit(chunk, post, group int64, lang string) store.AssistantHit {
	return store.AssistantHit{ChunkID: chunk, PostID: post, Group: group, Lang: lang, ChunkNo: int(chunk % 10)}
}

func ids(hs []Hit) []int64 {
	out := make([]int64, len(hs))
	for i, h := range hs {
		out[i] = h.ChunkID
	}
	return out
}

func TestFuseRRF(t *testing.T) {
	text := []store.AssistantHit{hit(1, 1, 1, "en"), hit(2, 2, 2, "en"), hit(3, 3, 3, "en")}
	vec := []store.AssistantHit{hit(3, 3, 3, "en"), hit(4, 4, 4, "en"), hit(1, 1, 1, "en")}
	got := Fuse(FuseOptions{}, text, vec)
	// 1: 1/61 + 1/63; 3: 1/63 + 1/61 (tie, lower post first); 2: 1/62; 4: 1/62.
	if want := []int64{1, 3, 2, 4}; !equal(ids(got), want) {
		t.Fatalf("order %v, want %v", ids(got), want)
	}
	if math.Abs(got[0].Fused-(1.0/61+1.0/63)) > 1e-12 {
		t.Errorf("score %v", got[0].Fused)
	}
	// Duplicates within a list count once, at their best rank.
	got = Fuse(FuseOptions{}, []store.AssistantHit{hit(1, 1, 1, "en"), hit(1, 1, 1, "en"), hit(2, 2, 2, "en")})
	if math.Abs(got[1].Fused-1.0/62) > 1e-12 {
		t.Errorf("duplicate shifted the ranks: %v", got[1].Fused)
	}
}

func TestFuseLanguageAndTranslations(t *testing.T) {
	// Post 10 (en) and 11 (es) translate each other (group 10); 20 is Spanish only.
	text := []store.AssistantHit{hit(101, 10, 10, "en"), hit(201, 20, 20, "es"), hit(111, 11, 10, "es")}
	got := Fuse(FuseOptions{Lang: "es", LangBoost: 1.25}, text)
	if want := []int64{201, 111}; !equal(ids(got), want) {
		t.Errorf("Spanish visitor: %v, want %v (translation in Spanish, English one dropped)", ids(got), want)
	}
	got = Fuse(FuseOptions{Lang: "en", LangBoost: 1.25}, text)
	if want := []int64{101, 201}; !equal(ids(got), want) {
		t.Errorf("English visitor: %v, want %v", ids(got), want)
	}
	// Without a version in the visitor's language the best one stays.
	got = Fuse(FuseOptions{Lang: "fr"}, text)
	if want := []int64{101, 201}; !equal(ids(got), want) {
		t.Errorf("French visitor: %v, want %v", ids(got), want)
	}
	// es-MX counts as Spanish.
	if !sameLang("es-MX", "es") || sameLang("", "") || sameLang("en", "es") {
		t.Error("sameLang")
	}
}

func TestFuseLimits(t *testing.T) {
	text := []store.AssistantHit{hit(1, 1, 1, "en"), hit(2, 1, 1, "en"), hit(3, 1, 1, "en"), hit(4, 2, 2, "en"), hit(5, 3, 3, "en")}
	got := Fuse(FuseOptions{PerPost: 2, Limit: 3}, text)
	if want := []int64{1, 2, 4}; !equal(ids(got), want) {
		t.Errorf("%v, want %v", ids(got), want)
	}
	if got := Fuse(FuseOptions{}); len(got) != 0 || got == nil {
		t.Errorf("no lists: %v", got)
	}
}

func equal(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
