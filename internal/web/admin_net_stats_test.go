package web

import "testing"

func TestStatsNumbers(t *testing.T) {
	for _, c := range []struct {
		lang      string
		n         int64
		num, comp string
	}{
		{"es", 0, "0", "0"}, {"es", 999, "999", "999"}, {"es", 1234, "1.234", "1.234"},
		{"en", 1234567, "1,234,567", "1.2M"}, {"es", 1234567, "1.234.567", "1,2M"},
		{"en", 45000, "45,000", "45k"}, {"es", 3_000_000_000, "3.000.000.000", "3B"}, {"en", -1200, "-1,200", "-1,200"},
	} {
		if got := fmtNum(c.lang, c.n); got != c.num {
			t.Errorf("fmtNum(%s, %d) = %q, want %q", c.lang, c.n, got, c.num)
		}
		if got := compactNum(c.lang, c.n); got != c.comp {
			t.Errorf("compactNum(%s, %d) = %q, want %q", c.lang, c.n, got, c.comp)
		}
	}
}
