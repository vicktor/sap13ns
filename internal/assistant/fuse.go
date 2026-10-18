package assistant

import (
	"sort"
	"strings"

	"sap13ns/internal/store"
)

// Reciprocal rank fusion merges the full-text and vector rankings without
// having to compare their scores: a chunk scores Σ 1/(rrfK + rank) over the
// lists it appears in.
const rrfK = 60

// FuseOptions shape the fused list.
type FuseOptions struct {
	Lang      string  // the visitor's language: its chunks are preferred
	LangBoost float64 // multiplies the score of chunks in Lang (1 = none)
	PerPost   int     // at most this many chunks of one post (0 = no limit)
	Limit     int     // at most this many chunks (0 = no limit)
}

// Hit is a fused search result.
type Hit struct {
	store.AssistantHit
	Fused float64
}

// Fuse combines ranked lists (best first) with RRF, then keeps one
// language per translation group (the visitor's when it was found, else
// the best-ranked one), at most opt.PerPost chunks per post and opt.Limit
// in all.
func Fuse(opt FuseOptions, lists ...[]store.AssistantHit) []Hit {
	if opt.LangBoost <= 0 {
		opt.LangBoost = 1
	}
	byChunk := map[int64]*Hit{}
	var order []*Hit
	for _, list := range lists {
		seen := map[int64]bool{}
		rank := 0
		for _, h := range list {
			if seen[h.ChunkID] {
				continue
			}
			seen[h.ChunkID] = true
			rank++
			hit := byChunk[h.ChunkID]
			if hit == nil {
				hit = &Hit{AssistantHit: h}
				byChunk[h.ChunkID] = hit
				order = append(order, hit)
			}
			hit.Fused += 1 / float64(rrfK+rank)
		}
	}
	for _, h := range order {
		if sameLang(h.Lang, opt.Lang) {
			h.Fused *= opt.LangBoost
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].Fused != order[j].Fused {
			return order[i].Fused > order[j].Fused
		}
		if order[i].PostID != order[j].PostID {
			return order[i].PostID < order[j].PostID
		}
		return order[i].ChunkNo < order[j].ChunkNo
	})

	// The post standing for each translation group.
	chosen := map[int64]int64{}
	for _, h := range order {
		if _, ok := chosen[h.Group]; !ok {
			chosen[h.Group] = h.PostID
		}
	}
	for _, h := range order {
		if sameLang(h.Lang, opt.Lang) {
			if cur := chosen[h.Group]; cur != h.PostID && !sameLang(langOf(order, cur), opt.Lang) {
				chosen[h.Group] = h.PostID
			}
		}
	}

	out := []Hit{}
	perPost := map[int64]int{}
	for _, h := range order {
		if chosen[h.Group] != h.PostID || opt.PerPost > 0 && perPost[h.PostID] >= opt.PerPost {
			continue
		}
		perPost[h.PostID]++
		out = append(out, *h)
		if opt.Limit > 0 && len(out) == opt.Limit {
			break
		}
	}
	return out
}

func langOf(hits []*Hit, postID int64) string {
	for _, h := range hits {
		if h.PostID == postID {
			return h.Lang
		}
	}
	return ""
}

// sameLang compares primary language subtags: "es" matches "es-MX".
func sameLang(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	pa, _, _ := strings.Cut(strings.ToLower(a), "-")
	pb, _, _ := strings.Cut(strings.ToLower(b), "-")
	return pa == pb
}
