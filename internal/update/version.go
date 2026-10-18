package update

import (
	"fmt"
	"strconv"
	"strings"
)

// version is a parsed semantic version. Build metadata is dropped: it does
// not take part in comparisons.
type version struct {
	major, minor, patch uint64
	pre                 []string // pre-release identifiers, nil for a release
}

// parseVersion accepts "1.2.3", "v1.2.3", "v1.2.3-rc.1", "1.2.3+build" and,
// leniently, "v1" or "v1.2" (missing parts are 0).
func parseVersion(s string) (version, error) {
	var v version
	orig := s
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	if s == "" {
		return v, fmt.Errorf("update: bad version %q", orig)
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.' || r == '-' || r == '+') {
			return v, fmt.Errorf("update: bad version %q", orig)
		}
	}
	s, build, hasBuild := strings.Cut(s, "+")
	if hasBuild && (build == "" || strings.Contains(build, "+") || !validIdents(build)) {
		return v, fmt.Errorf("update: bad build metadata in version %q", orig)
	}
	core, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) > 3 {
		return v, fmt.Errorf("update: bad version %q", orig)
	}
	nums := [3]uint64{}
	for i, p := range parts {
		if p == "" || !isDigits(p) {
			return v, fmt.Errorf("update: bad version %q", orig)
		}
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return v, fmt.Errorf("update: bad version %q", orig)
		}
		nums[i] = n
	}
	v.major, v.minor, v.patch = nums[0], nums[1], nums[2]
	if hasPre {
		if !validIdents(pre) {
			return v, fmt.Errorf("update: bad pre-release in version %q", orig)
		}
		v.pre = strings.Split(pre, ".")
	}
	return v, nil
}

// validIdents reports whether s is a dot-separated list of non-empty
// identifiers.
func validIdents(s string) bool {
	if s == "" {
		return false
	}
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return false
		}
	}
	return true
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// compare follows semver 2.0.0 precedence: a pre-release is lower than the
// release with the same core version.
func (a version) compare(b version) int {
	if c := cmpUint(a.major, b.major); c != 0 {
		return c
	}
	if c := cmpUint(a.minor, b.minor); c != 0 {
		return c
	}
	if c := cmpUint(a.patch, b.patch); c != 0 {
		return c
	}
	switch {
	case a.pre == nil && b.pre == nil:
		return 0
	case a.pre == nil:
		return 1
	case b.pre == nil:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		x, y := a.pre[i], b.pre[i]
		xn, yn := isDigits(x), isDigits(y)
		switch {
		case xn && yn:
			xi, _ := strconv.ParseUint(x, 10, 64)
			yi, _ := strconv.ParseUint(y, 10, 64)
			if c := cmpUint(xi, yi); c != 0 {
				return c
			}
		case xn:
			return -1 // numeric identifiers sort before alphanumeric ones
		case yn:
			return 1
		default:
			if c := strings.Compare(x, y); c != 0 {
				return c
			}
		}
	}
	return cmpUint(uint64(len(a.pre)), uint64(len(b.pre)))
}

// Compare compares two semantic versions ("v" prefix optional) and returns
// -1, 0 or +1. Pre-releases sort before the release ("v1.2.0-rc.1" <
// "v1.2.0"); build metadata is ignored.
func Compare(a, b string) (int, error) {
	va, err := parseVersion(a)
	if err != nil {
		return 0, err
	}
	vb, err := parseVersion(b)
	if err != nil {
		return 0, err
	}
	return va.compare(vb), nil
}

// IsNewer reports whether latest is a newer version than current. A current
// version that is not a semantic version (such as "dev" for local builds) is
// never considered out of date.
func IsNewer(latest, current string) bool {
	c, err := Compare(latest, current)
	return err == nil && c > 0
}

// sameVersion reports whether a and b are the same version string, ignoring
// a "v" prefix.
func sameVersion(a, b string) bool {
	a = strings.TrimPrefix(strings.TrimSpace(a), "v")
	b = strings.TrimPrefix(strings.TrimSpace(b), "v")
	return a != "" && a == b
}
