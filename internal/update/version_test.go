package update

import "testing"

func TestCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"v1.2.3", "1.2.3", 0},
		{"v1.2.3", "v1.2.4", -1},
		{"v1.10.0", "v1.9.9", 1},
		{"v2.0.0", "v1.99.99", 1},
		{"v1.2", "v1.2.0", 0},
		{"v1", "v1.0.1", -1},
		{"v1.2.3-rc.1", "v1.2.3", -1},
		{"v1.2.3", "v1.2.3-rc.1", 1},
		{"v1.2.3-alpha", "v1.2.3-alpha.1", -1},
		{"v1.2.3-alpha.1", "v1.2.3-alpha.beta", -1},
		{"v1.2.3-alpha.beta", "v1.2.3-beta", -1},
		{"v1.2.3-beta.2", "v1.2.3-beta.11", -1},
		{"v1.2.3-rc.1", "v1.2.3-rc.1", 0},
		{"v1.2.3+build.5", "v1.2.3", 0},
		{"v1.2.4-rc.1", "v1.2.3", 1},
	}
	for _, tt := range tests {
		got, err := Compare(tt.a, tt.b)
		if err != nil {
			t.Errorf("Compare(%q, %q): %v", tt.a, tt.b, err)
			continue
		}
		if got != tt.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestParseVersionRejects(t *testing.T) {
	for _, s := range []string{"", "v", "dev", "1.2.3.4", "1..2", "v1.2.x", "1.2.3-", "1.2.3+", "1.2.3-rc..1", "1.2.3/../x", "1.2.3 beta", "-1.2.3"} {
		if _, err := parseVersion(s); err == nil {
			t.Errorf("parseVersion(%q) accepted", s)
		}
	}
}

func TestIsNewer(t *testing.T) {
	tests := []struct {
		latest, current string
		want            bool
	}{
		{"v1.2.0", "v1.1.9", true},
		{"v1.2.0", "1.2.0", false},
		{"v1.1.0", "v1.2.0", false},
		{"v1.2.0", "v1.2.0-rc.2", true},
		{"v1.2.0", "dev", false},
		{"v1.2.0", "", false},
		{"garbage", "v1.0.0", false},
	}
	for _, tt := range tests {
		if got := IsNewer(tt.latest, tt.current); got != tt.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", tt.latest, tt.current, got, tt.want)
		}
	}
}

func TestSameVersion(t *testing.T) {
	if !sameVersion("v1.2.3", "1.2.3") || !sameVersion("1.2.3", "1.2.3") {
		t.Error("same versions not equal")
	}
	if sameVersion("v1.2.3", "v1.2.4") || sameVersion("", "") || sameVersion("v1.2.3", "v1.2.3+x") {
		t.Error("different versions equal")
	}
}
