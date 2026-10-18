package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	maxAPIResponse = 1 << 20  // GitHub API response body
	maxNotes       = 64 << 10 // Release.Notes
	checkTimeout   = 30 * time.Second
)

// Release is a published GitHub release. Treat it as read-only.
type Release struct {
	Version   string    // tag name, e.g. "v1.4.0"
	Published time.Time // zero if GitHub did not say
	Notes     string    // release body (Markdown, untrusted), capped at 64 KiB
	URL       string    // release page (https://github.com/...), "" if invalid
	Assets    []Asset
}

// Asset is a file attached to a release.
type Asset struct {
	Name string
	URL  string // browser_download_url
	Size int64
}

// asset returns the asset called name, or nil.
func (r *Release) asset(name string) *Asset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

func (r *Release) clone() *Release {
	if r == nil {
		return nil
	}
	c := *r
	c.Assets = append([]Asset(nil), r.Assets...)
	return &c
}

var repoRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// Check asks the GitHub API for the latest release of repo ("owner/name",
// normally DefaultRepo) and reports whether it is newer than current, the
// running version. Drafts and prereleases are never returned; ErrNoRelease
// means there is nothing published yet. A current version that is not a
// semantic version ("dev") is never out of date, but the latest release is
// still returned.
//
// c may be nil (NewHTTPClient is used); it is not modified. Requests and
// redirects go only to https://api.github.com. Without a deadline on ctx the
// check gives up after 30 seconds.
func Check(ctx context.Context, c *http.Client, repo, current string) (*Release, bool, error) {
	if !repoRE.MatchString(repo) || strings.Contains(repo, "..") {
		return nil, false, fmt.Errorf("update: bad repository %q, want owner/name", repo)
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, checkTimeout)
		defer cancel()
	}
	u := "https://api.github.com/repos/" + repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, false, fmt.Errorf("update: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", userAgent(current))
	resp, err := restrictClient(c, apiHosts).Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("update: checking %s: %w", repo, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, false, ErrNoRelease
	case http.StatusForbidden, http.StatusTooManyRequests:
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			msg := "update: GitHub API rate limit exceeded"
			if n, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
				msg += "; it resets at " + time.Unix(n, 0).UTC().Format(time.RFC3339)
			}
			return nil, false, fmt.Errorf("%s", msg)
		}
		return nil, false, fmt.Errorf("update: GitHub API returned %s", resp.Status)
	default:
		return nil, false, fmt.Errorf("update: GitHub API returned %s", resp.Status)
	}
	body, err := readLimited(resp.Body, maxAPIResponse)
	if err != nil {
		return nil, false, fmt.Errorf("update: reading GitHub API response: %w", err)
	}
	var raw apiRelease
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, false, fmt.Errorf("update: decoding GitHub API response: %w", err)
	}
	if raw.Draft || raw.Prerelease {
		return nil, false, ErrNoRelease
	}
	rel, err := raw.release()
	if err != nil {
		return nil, false, err
	}
	return rel, IsNewer(rel.Version, current), nil
}

// apiRelease is the subset of the GitHub release object that is used.
type apiRelease struct {
	TagName     string    `json:"tag_name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	Assets      []struct {
		Name  string `json:"name"`
		URL   string `json:"browser_download_url"`
		Size  int64  `json:"size"`
		State string `json:"state"`
	} `json:"assets"`
}

func (a *apiRelease) release() (*Release, error) {
	tag := strings.TrimSpace(a.TagName)
	v, err := parseVersion(tag)
	if err != nil {
		return nil, fmt.Errorf("update: latest release has a tag that is not a version (%q)", a.TagName)
	}
	if v.pre != nil {
		return nil, ErrNoRelease // a pre-release tag not flagged as such
	}
	rel := &Release{Version: tag, Published: a.PublishedAt, Notes: capNotes(a.Body)}
	if u, err := checkRawURL(a.HTMLURL, downloadHosts); err == nil && strings.EqualFold(u.Hostname(), "github.com") {
		rel.URL = u.String()
	}
	for _, as := range a.Assets {
		if as.Name == "" || as.URL == "" || as.Size < 0 || (as.State != "" && as.State != "uploaded") {
			continue
		}
		rel.Assets = append(rel.Assets, Asset{Name: as.Name, URL: as.URL, Size: as.Size})
	}
	return rel, nil
}

// capNotes truncates s to maxNotes bytes without splitting a UTF-8 sequence.
func capNotes(s string) string {
	if len(s) <= maxNotes {
		return s
	}
	return strings.ToValidUTF8(s[:maxNotes], "")
}
