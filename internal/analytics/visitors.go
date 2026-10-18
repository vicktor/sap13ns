package analytics

// visitorSet counts the distinct visitor hashes of one day. It is exact up
// to max entries; beyond that it switches to adaptive (distinct) sampling:
// at level k it keeps only hashes whose top k bits are zero, each standing
// for 2^k visitors, and raises k whenever the set outgrows max. Memory stays
// bounded by max and the estimate's relative error is about 1/sqrt(max/2).
type visitorSet struct {
	max   int
	level uint
	m     map[uint64]struct{}
}

func newVisitorSet(max int) *visitorSet {
	return &visitorSet{max: max, m: make(map[uint64]struct{})}
}

// add records a visitor hash, thinning the sample when the set is full.
func (s *visitorSet) add(h uint64) {
	if !s.sampled(h) {
		return
	}
	s.m[h] = struct{}{}
	for len(s.m) > s.max && s.level < 63 {
		s.level++
		for k := range s.m {
			if !s.sampled(k) {
				delete(s.m, k)
			}
		}
	}
}

func (s *visitorSet) sampled(h uint64) bool {
	return s.level == 0 || h>>(64-s.level) == 0
}

// count is the number of distinct visitors (an estimate once sampling).
func (s *visitorSet) count() int64 { return int64(len(s.m)) << s.level }

// exact reports whether count is exact.
func (s *visitorSet) exact() bool { return s.level == 0 }
