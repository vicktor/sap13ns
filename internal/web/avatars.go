package web

// Profile photos. A person shows, in this order: the photo they uploaded,
// the copy of their Gravatar when the site uses Gravatar and they have
// one, or their initials.
//
// Visitors' browsers never contact Gravatar. The server looks a Gravatar
// up itself, through the SSRF guard (internal/netguard), with the SHA-256
// of the trimmed, lower-cased e-mail address, and serves a copy from the
// site's uploads, re-encoded like an upload. It looks users up when they
// are created, when their address changes, when they ask (Refresh from
// Gravatar) and weekly in the background; commenters only when the site
// shows their Gravatars (Settings → Discussion), when they are shown, into
// a bounded cache. A page never waits for Gravatar.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/media"
	"sap13ns/internal/netguard"
	"sap13ns/internal/store"
)

// AvatarView is what templates show for a person: a square photo in
// three sizes, or their initials when they have none (URL is then "").
// The photo is decorative: the person's name goes next to it.
type AvatarView struct {
	URL      string // 64 × 64
	URL2x    string // 128 × 128
	Large    string // 256 × 256
	Initials string
}

// Srcset lists the three sizes with their widths, for an <img sizes>.
func (a AvatarView) Srcset() string {
	if a.URL == "" {
		return ""
	}
	return a.URL + " 64w, " + a.URL2x + " 128w, " + a.Large + " 256w"
}

// avatarView is the avatar of a person named name whose photo is key ("",
// or a key that is not an avatar's, shows the initials).
func avatarView(name, key string) AvatarView {
	v := AvatarView{Initials: initials(name)}
	if media.ValidAvatarKey(key) {
		v.URL, v.URL2x, v.Large = avatarURL(key, 64), avatarURL(key, 128), avatarURL(key, 256)
	}
	return v
}

func avatarURL(key string, size int) string { return "/uploads/" + media.AvatarFile(key, size) }

// photoOf picks a user's photo: the one they uploaded, else their Gravatar
// copy while the site uses Gravatar.
func photoOf(set store.Settings, avatar, gravatar string) string {
	if avatar != "" || !set.GravatarUsers {
		return avatar
	}
	return gravatar
}

func (s *Server) userAvatar(u *store.User) AvatarView {
	if u == nil {
		return avatarView("", "")
	}
	return avatarView(u.Name(), photoOf(s.settings(), u.Avatar, u.Gravatar))
}

// commentAvatar is the avatar of a comment's author: a user's own avatar
// (whatever the setting for commenters), else the copy of the commenter's
// Gravatar that commenterGravatars found.
func commentAvatar(set store.Settings, c *store.Comment) AvatarView {
	if c.UserID != nil {
		return avatarView(c.AuthorName, photoOf(set, c.UserAvatar, c.UserGravatar))
	}
	if !showCommenterGravatars(set) {
		return avatarView(c.AuthorName, "")
	}
	return avatarView(c.AuthorName, c.Gravatar)
}

func showCommenterGravatars(set store.Settings) bool { return set.GravatarUsers && set.GravatarCommenters }

// UserAvatar is the avatar of a user (*store.User or store.User) for the
// admin templates: {{template "avatar" (dict "A" ($.UserAvatar .) …)}}.
func (p *adminPage) UserAvatar(u any) AvatarView {
	switch v := u.(type) {
	case *store.User:
		if v != nil {
			return avatarView(v.Name(), photoOf(p.Site, v.Avatar, v.Gravatar))
		}
	case store.User:
		return avatarView(v.Name(), photoOf(p.Site, v.Avatar, v.Gravatar))
	}
	return avatarView("", "")
}

// CommentAvatar is the avatar of a comment's author, for the admin
// templates; the handler looked the commenters' Gravatars up.
func (p *adminPage) CommentAvatar(c store.Comment) AvatarView { return commentAvatar(p.Site, &c) }

// ------------------------------------------------------------- uploads --

// avatarBodyLimit bounds the request of a photo upload: the photo and the
// form around it.
const avatarBodyLimit = media.AvatarMaxBytes + 64<<10

// isAvatarUpload reports whether r uploads a photo (routes.go).
func isAvatarUpload(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/admin/") && strings.HasSuffix(r.URL.Path, "/avatar")
}

// avatarTarget is the user whose photo a request changes, and how it
// answers.
type avatarTarget struct {
	user *store.User
	back string                 // where it redirects
	fail func(int, string)      // renders the page again with an error
	ok   func(kind, msg string) // redirects back with a message
}

func (s *Server) profileTarget(w http.ResponseWriter, r *http.Request) *avatarTarget {
	u := currentUser(r)
	t := &avatarTarget{user: u, back: "/admin/profile",
		fail: func(status int, msg string) { s.renderProfile(w, r, status, u, "profile", msg) }}
	t.ok = func(kind, msg string) { s.redirect(w, r, t.back, kind, msg) }
	return t
}

// userTarget is the user of an edit page, for an administrator with
// users.manage (routes.go): the account of a network admin on the portal
// stays protected (refuseProtected).
func (s *Server) userTarget(w http.ResponseWriter, r *http.Request) *avatarTarget {
	u, ok := s.loadUser(w, r)
	if !ok || s.refuseProtected(w, r, u) {
		return nil
	}
	t := &avatarTarget{user: u, back: fmt.Sprintf("/admin/users/%d", u.ID)}
	t.fail = func(status int, msg string) {
		d, err := s.userEditData(r, u, msg)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		s.renderAdminStatus(w, r, status, "user_edit", u.Name(), "users", d)
	}
	t.ok = func(kind, msg string) { s.redirect(w, r, t.back, kind, msg) }
	return t
}

func (s *Server) adminProfileAvatar(w http.ResponseWriter, r *http.Request) {
	s.avatarUpload(w, r, s.profileTarget(w, r))
}

func (s *Server) adminProfileAvatarDelete(w http.ResponseWriter, r *http.Request) {
	s.avatarDelete(w, r, s.profileTarget(w, r))
}

func (s *Server) adminProfileGravatar(w http.ResponseWriter, r *http.Request) {
	s.gravatarRefresh(w, r, s.profileTarget(w, r))
}

func (s *Server) adminUserAvatar(w http.ResponseWriter, r *http.Request) {
	if t := s.userTarget(w, r); t != nil {
		s.avatarUpload(w, r, t)
	}
}

func (s *Server) adminUserAvatarDelete(w http.ResponseWriter, r *http.Request) {
	if t := s.userTarget(w, r); t != nil {
		s.avatarDelete(w, r, t)
	}
}

func (s *Server) adminUserGravatar(w http.ResponseWriter, r *http.Request) {
	if t := s.userTarget(w, r); t != nil {
		s.gravatarRefresh(w, r, t)
	}
}

// avatarUpload stores the photo of the form's "photo" file: 413 when it is
// too large or the site's storage is full, 422 when it is not a photo.
func (s *Server) avatarUpload(w http.ResponseWriter, r *http.Request, t *avatarTarget) {
	ctx := r.Context()
	u := t.user
	if !s.uploadUser.Allow(fmt.Sprint(uid(r))) {
		t.fail(http.StatusTooManyRequests, s.t("Too many uploads. Try again later."))
		return
	}
	f, fh, err := r.FormFile("photo")
	if err != nil {
		t.fail(http.StatusUnprocessableEntity, s.t("Choose a photo to upload."))
		return
	}
	defer f.Close()
	tooLarge := s.t("The photo is too large (%s max).", filesize(media.AvatarMaxBytes))
	if fh.Size > media.AvatarMaxBytes {
		t.fail(http.StatusRequestEntityTooLarge, tooLarge)
		return
	}
	a, err := media.MakeAvatar(ctx, f, media.AvatarMaxBytes)
	switch {
	case errors.Is(err, media.ErrTooLarge):
		t.fail(http.StatusRequestEntityTooLarge, tooLarge)
		return
	case errors.Is(err, media.ErrUnsupported):
		t.fail(http.StatusUnprocessableEntity, s.t("Choose a JPEG, PNG, WebP or GIF image."))
		return
	case errors.Is(err, media.ErrBadImage):
		t.fail(http.StatusUnprocessableEntity, s.t("The image could not be read, or it is larger than %d megapixels.", media.AvatarMaxPixels/1_000_000))
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	key, err := s.media.SaveAvatar(ctx, a, "u"+strconv.FormatInt(u.ID, 10))
	if errors.Is(err, media.ErrQuota) {
		t.fail(http.StatusRequestEntityTooLarge, s.storageFull())
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	old, err := s.store.SetUserAvatar(ctx, u.ID, key)
	if err != nil {
		s.media.RemoveAvatar(ctx, key)
		s.serverError(w, r, err)
		return
	}
	s.media.RemoveAvatar(ctx, old)
	// The photo wins: the Gravatar copy goes, and no lookup is made while
	// the photo is there.
	if g, err := s.store.ForgetGravatar(ctx, u.ID); err == nil {
		s.media.RemoveAvatar(ctx, g)
	}
	s.audit(r, uid(r), "user.photo_uploaded", u.Username, nil)
	s.contentChanged()
	t.ok("success", s.t("Photo saved."))
}

func (s *Server) avatarDelete(w http.ResponseWriter, r *http.Request, t *avatarTarget) {
	ctx := r.Context()
	old, err := s.store.SetUserAvatar(ctx, t.user.ID, "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if old == "" {
		t.ok("", "")
		return
	}
	s.media.RemoveAvatar(ctx, old)
	s.audit(r, uid(r), "user.photo_removed", t.user.Username, nil)
	s.contentChanged()
	s.gravatarSoon(t.user.ID)
	t.ok("success", s.t("Photo removed."))
}

// gravatarRefresh looks the user's Gravatar up now, on the button of the
// profile: an action of its own, never a page waiting.
func (s *Server) gravatarRefresh(w http.ResponseWriter, r *http.Request, t *avatarTarget) {
	if !s.settings().GravatarUsers {
		t.ok("error", s.t("Gravatar is switched off for this site (Settings → Discussion)."))
		return
	}
	if t.user.Avatar != "" {
		t.ok("", "")
		return
	}
	if !s.grav.refreshes.Allow(fmt.Sprint(uid(r))) {
		t.fail(http.StatusTooManyRequests, s.t("Too many attempts. Wait a few minutes and try again."))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	found, err := s.lookupUserGravatar(ctx, t.user.ID)
	switch {
	case err != nil:
		s.log.Warn("gravatar lookup", "user", t.user.Username, "err", err)
		t.ok("warning", s.t("Gravatar could not be reached. Try again later."))
	case found:
		t.ok("success", s.t("The Gravatar of this e-mail address is shown."))
	default:
		t.ok("info", s.t("Gravatar has no image for this e-mail address."))
	}
}

// emailChanged follows a change of the e-mail address of u (as it was) to
// email: the store dropped the copy of the old address's Gravatar
// (UpdateUserProfile), so its files go, and the new address is looked up.
func (s *Server) emailChanged(r *http.Request, u *store.User, email string) {
	if strings.EqualFold(strings.TrimSpace(u.Email), strings.TrimSpace(email)) {
		return
	}
	if u.Gravatar != "" {
		if now, err := s.store.UserByID(r.Context(), u.ID); err == nil && now.Gravatar != u.Gravatar {
			s.media.RemoveAvatar(r.Context(), u.Gravatar)
		}
	}
	s.gravatarSoon(u.ID)
}

// removeUserPhotos deletes the files of a deleted user's photo and
// Gravatar copy.
func (s *Server) removeUserPhotos(ctx context.Context, u *store.User) {
	s.media.RemoveAvatar(ctx, u.Avatar)
	s.media.RemoveAvatar(ctx, u.Gravatar)
}

// ------------------------------------------------------------ Gravatar --

// gravatarURL is where Gravatar serves images (docs.gravatar.com): a
// SHA-256 hash, the size, and d=404, which answers 404 instead of a
// default picture when the address has none. The default rating, G, is
// kept.
const gravatarURL = "https://gravatar.com/avatar/"

// gravatarMaxBytes bounds the image Gravatar sends: 256 px is small.
const gravatarMaxBytes = 1 << 20

var errNoGravatar = errors.New("no gravatar for this address")

// gravatarHash is the Gravatar hash of an e-mail address: the SHA-256 of
// the trimmed, lower-cased address. Only this hash leaves the server.
func gravatarHash(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return hex.EncodeToString(sum[:])
}

// realAddress reports whether an e-mail address may belong to someone: not
// under a domain reserved for examples and tests (RFC 2606 and 6761),
// such as the example.org addresses of seeded sites or the .invalid ones
// of restored users.
func realAddress(email string) bool {
	_, domain, ok := strings.Cut(strings.ToLower(strings.TrimSpace(email)), "@")
	if !ok || domain == "" {
		return false
	}
	domain = "." + strings.TrimSuffix(domain, ".")
	for _, reserved := range []string{".example.com", ".example.net", ".example.org", ".test", ".example", ".invalid", ".localhost", ".local"} {
		if strings.HasSuffix(domain, reserved) {
			return false
		}
	}
	return true
}

// A gravatarFetcher downloads Gravatars through the SSRF guard, which
// resolves the host itself and connects only to the addresses allow
// accepts (netguard.Public when nil), on every redirect too.
type gravatarFetcher struct {
	base   string // gravatarURL; a stand-in server in tests and development
	client *http.Client
}

func newGravatarFetcher(base string, allow func(netip.Addr) bool) *gravatarFetcher {
	return &gravatarFetcher{base: base, client: &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext:           netguard.Dialer(allow, nil, 5*time.Second),
			Proxy:                 nil,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 8 * time.Second,
			MaxIdleConns:          2,
			IdleConnTimeout:       30 * time.Second,
			ForceAttemptHTTP2:     true,
		},
		// Gravatar answers the image or 404 itself.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

var defaultGravatar = sync.OnceValue(func() *gravatarFetcher { return newGravatarFetcher(gravatarURL, nil) })

// fetch downloads the Gravatar of hash and makes its avatar: errNoGravatar
// when Gravatar has none.
func (g *gravatarFetcher) fetch(ctx context.Context, hash string) (*media.Avatar, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.base+hash+"?s=256&d=404", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "sap13ns/"+Version)
	req.Header.Set("Accept", "image/*")
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, errNoGravatar
	default:
		return nil, fmt.Errorf("gravatar: %s", resp.Status)
	}
	return media.MakeAvatar(ctx, resp.Body, gravatarMaxBytes)
}

// gravatarState is the server's Gravatar machinery.
type gravatarState struct {
	fetcher *gravatarFetcher // tests and development: a stand-in server

	mu      sync.Mutex
	pending map[string]bool // lookups running: "u<id>", or a commenter's hash
	slots   chan struct{}   // lookups at once
	kick    chan struct{}   // runs the background pass soon

	refreshes  *auth.Limiter // Refresh from Gravatar, per user
	commenters *auth.Limiter // commenters' lookups, per site
}

func newGravatarState() gravatarState {
	return gravatarState{pending: map[string]bool{}, slots: make(chan struct{}, 2), kick: make(chan struct{}, 1),
		refreshes: auth.NewLimiter(10, time.Hour), commenters: auth.NewLimiter(120, time.Hour)}
}

func (s *Server) gravatarFetcher() *gravatarFetcher {
	if s.grav.fetcher != nil {
		return s.grav.fetcher
	}
	if s.cfg.Dev && s.cfg.DevGravatarURL != "" {
		s.grav.mu.Lock()
		defer s.grav.mu.Unlock()
		if s.grav.fetcher == nil {
			// A stand-in server, which may run on this machine.
			s.grav.fetcher = newGravatarFetcher(strings.TrimSuffix(s.cfg.DevGravatarURL, "/")+"/", func(a netip.Addr) bool {
				return netguard.Public(a) || a.Unmap().IsLoopback()
			})
		}
		return s.grav.fetcher
	}
	return defaultGravatar()
}

// begin claims a lookup, so that the same one never runs twice at once.
func (g *gravatarState) begin(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pending[key] {
		return false
	}
	g.pending[key] = true
	return true
}

func (g *gravatarState) end(key string) {
	g.mu.Lock()
	delete(g.pending, key)
	g.mu.Unlock()
}

// lookupUserGravatar looks a user's Gravatar up and records what it
// found; it reports whether the user has one. A user with a photo of
// their own, or a site without Gravatar, sends nothing.
func (s *Server) lookupUserGravatar(ctx context.Context, id int64) (bool, error) {
	if !s.settings().GravatarUsers {
		return false, nil
	}
	u, err := s.store.UserByID(ctx, id)
	if err != nil || u.Avatar != "" {
		return false, err
	}
	if !realAddress(u.Email) {
		// Nobody can have a Gravatar there: nothing is sent.
		_, _, err := s.store.SetUserGravatar(ctx, id, u.Email, "")
		return false, err
	}
	select {
	case s.grav.slots <- struct{}{}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	defer func() { <-s.grav.slots }()
	a, err := s.gravatarFetcher().fetch(ctx, gravatarHash(u.Email))
	if !s.settings().GravatarUsers { // switched off meanwhile
		return false, nil
	}
	key := ""
	switch {
	case errors.Is(err, errNoGravatar):
	case err != nil:
		if rerr := s.store.RetryGravatar(context.WithoutCancel(ctx), id, time.Now().Add(24*time.Hour)); rerr != nil {
			s.log.Error("gravatar retry", "err", rerr)
		}
		return false, err
	default:
		if key, err = s.media.SaveAvatar(ctx, a, "g"+strconv.FormatInt(id, 10)); err != nil {
			// The site's storage is full, most likely: the initials stay.
			s.store.RetryGravatar(context.WithoutCancel(ctx), id, time.Now().Add(24*time.Hour))
			return false, err
		}
	}
	old, ok, err := s.store.SetUserGravatar(ctx, id, u.Email, key)
	if err != nil || !ok {
		s.media.RemoveAvatar(ctx, key) // the address changed meanwhile
		return false, err
	}
	if old != key {
		s.media.RemoveAvatar(ctx, old)
	}
	if old != "" || key != "" {
		s.contentChanged()
	}
	return key != "", nil
}

// gravatarSoon looks a user's Gravatar up in the background: a new user,
// a new e-mail address, a photo removed.
func (s *Server) gravatarSoon(id int64) {
	if !s.settings().GravatarUsers {
		return
	}
	key := "u" + strconv.FormatInt(id, 10)
	if !s.grav.begin(key) {
		return
	}
	s.goJob(func() {
		defer s.grav.end(key)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := s.lookupUserGravatar(ctx, id); err != nil {
			s.log.Warn("gravatar lookup", "user", id, "err", err)
		}
	})
}

// The cache of commenters' Gravatars.
const (
	commenterCacheRows  = 1000
	commenterCacheBytes = 32 << 20
	commenterRecheck    = 14 * 24 * time.Hour // a lookup is redone when shown after this
	commenterUnused     = 60 * 24 * time.Hour // and dropped when not shown for this long
	commenterPerPage    = 10                  // lookups one page view may start
)

// commenterGravatars fills in the Gravatar copies of comments by visitors
// (not users) when the site shows commenters' Gravatars, from the cache
// only. The lookups it lacks, or that are old, start in the background,
// for the next view.
func (s *Server) commenterGravatars(ctx context.Context, list []*store.Comment) {
	if !showCommenterGravatars(s.settings()) {
		return
	}
	byHash := map[string][]*store.Comment{}
	var hashes []string
	for _, c := range list {
		// A spammer's address is never sent to Gravatar.
		if c.UserID != nil || c.Status == "spam" || c.Status == "trash" || !realAddress(c.AuthorEmail) {
			continue
		}
		h := gravatarHash(c.AuthorEmail)
		if byHash[h] == nil {
			hashes = append(hashes, h)
		}
		byHash[h] = append(byHash[h], c)
	}
	if len(hashes) == 0 {
		return
	}
	found, err := s.store.CommenterGravatars(ctx, hashes)
	if err != nil {
		s.log.Error("commenter gravatars", "err", err)
		return
	}
	now := time.Now()
	var stale, used []string
	for _, h := range hashes {
		g, ok := found[h]
		if !ok || now.Sub(g.CheckedAt) > commenterRecheck {
			stale = append(stale, h)
		}
		if ok && now.Sub(g.UsedAt) > time.Hour {
			used = append(used, h)
		}
		for _, c := range byHash[h] {
			c.Gravatar = g.Path
		}
	}
	if err := s.store.TouchCommenterGravatars(ctx, used); err != nil {
		s.log.Error("commenter gravatars", "err", err)
	}
	s.queueCommenters(stale)
}

// commentPtrs points at the comments of a list, for commenterGravatars.
func commentPtrs(list []store.Comment) []*store.Comment {
	out := make([]*store.Comment, len(list))
	for i := range list {
		out[i] = &list[i]
	}
	return out
}

// queueCommenters looks commenters' Gravatars up in the background, a few
// at a time and at most commenters an hour.
func (s *Server) queueCommenters(hashes []string) {
	var todo []string
	for _, h := range hashes {
		if len(todo) == commenterPerPage {
			break
		}
		if s.grav.begin(h) {
			if !s.grav.commenters.Allow("site") {
				s.grav.end(h)
				break
			}
			todo = append(todo, h)
		}
	}
	if len(todo) == 0 {
		return
	}
	s.goJob(func() {
		found := false
		for _, h := range todo {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			ok, err := s.lookupCommenter(ctx, h)
			cancel()
			s.grav.end(h)
			if err != nil {
				s.log.Warn("gravatar lookup", "err", err)
			}
			found = found || ok
		}
		if found {
			s.pages.Invalidate() // the cached pages show the initials
		}
	})
}

// lookupCommenter looks a commenter's Gravatar up into the cache, which it
// keeps within its bounds; it reports whether one was found.
func (s *Server) lookupCommenter(ctx context.Context, hash string) (bool, error) {
	if !showCommenterGravatars(s.settings()) {
		return false, nil
	}
	select {
	case s.grav.slots <- struct{}{}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	defer func() { <-s.grav.slots }()
	a, err := s.gravatarFetcher().fetch(ctx, hash)
	if !showCommenterGravatars(s.settings()) {
		return false, nil
	}
	key, n := "", int64(0)
	switch {
	case errors.Is(err, errNoGravatar):
	case err != nil:
		return false, err
	default:
		if key, err = s.media.SaveAvatar(ctx, a, "c"); err != nil {
			return false, err
		}
		n = a.Bytes()
	}
	old, err := s.store.PutCommenterGravatar(ctx, hash, key, n)
	if err != nil {
		s.media.RemoveAvatar(ctx, key)
		return false, err
	}
	s.media.RemoveAvatar(ctx, old)
	evicted, err := s.store.EvictCommenterGravatars(ctx, commenterCacheRows, commenterCacheBytes, time.Now().Add(-commenterUnused))
	for _, k := range evicted {
		s.media.RemoveAvatar(ctx, k)
	}
	return key != "", err
}

// gravatarSettingsChanged applies the Gravatar settings when they change:
// switched off, the copies go, and nothing more is sent; switched on, the
// users are looked up by the background pass, soon.
func (s *Server) gravatarSettingsChanged(ctx context.Context, before, after store.Settings) {
	if before.GravatarUsers && !after.GravatarUsers {
		s.clearGravatars(ctx, true)
	} else if showCommenterGravatars(before) && !showCommenterGravatars(after) {
		s.clearGravatars(ctx, false)
	}
	if !before.GravatarUsers && after.GravatarUsers {
		select {
		case s.grav.kick <- struct{}{}:
		default:
		}
	}
}

// clearGravatars deletes the commenters' Gravatar copies, and the users'
// too when users is true.
func (s *Server) clearGravatars(ctx context.Context, users bool) {
	var keys []string
	if users {
		k, err := s.store.ClearGravatars(ctx)
		if err != nil {
			s.log.Error("clear gravatars", "err", err)
		}
		keys = k
	}
	k, err := s.store.ClearCommenterGravatars(ctx)
	if err != nil {
		s.log.Error("clear gravatars", "err", err)
	}
	for _, key := range append(keys, k...) {
		s.media.RemoveAvatar(ctx, key)
	}
	if len(keys)+len(k) > 0 {
		s.contentChanged()
	}
}

// The background pass (runGravatars).
const (
	gravatarEvery     = 15 * time.Minute // between passes, ± half of it
	gravatarPerPass   = 10               // users looked up in a pass
	gravatarSweepEach = 6 * time.Hour    // between sweeps of stray files
)

// runGravatars is the low-rate background work of avatars, until ctx
// ends: every quarter of an hour or so (with jitter), the users due are
// looked up (never looked up, or a week ago), at most gravatarPerPass, a
// second or more apart; the commenters' cache is kept within its bounds;
// and every few hours the files no row names any more are deleted.
func (s *Server) runGravatars(ctx context.Context) {
	wait := time.Minute + rand.N(5*time.Minute)
	var swept time.Time
	for {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		case <-s.grav.kick:
			t.Stop()
		}
		now := time.Now()
		s.gravatarPass(ctx, now)
		if now.Sub(swept) >= gravatarSweepEach {
			s.sweepAvatars(ctx, now)
			swept = now
		}
		wait = gravatarEvery/2 + rand.N(gravatarEvery)
	}
}

// gravatarPass is one pass of runGravatars.
func (s *Server) gravatarPass(ctx context.Context, now time.Time) {
	set := s.settings()
	if !set.GravatarUsers {
		s.clearGravatars(ctx, true) // what a restore or another process left
		return
	}
	if !showCommenterGravatars(set) {
		s.clearGravatars(ctx, false)
	} else {
		evicted, err := s.store.EvictCommenterGravatars(ctx, commenterCacheRows, commenterCacheBytes, now.Add(-commenterUnused))
		if err != nil {
			s.log.Error("evict gravatars", "err", err)
		}
		for _, k := range evicted {
			s.media.RemoveAvatar(ctx, k)
		}
	}
	due, err := s.store.GravatarDue(ctx, now.Add(-store.GravatarRefresh), gravatarPerPass)
	if err != nil {
		s.log.Error("gravatars due", "err", err)
		return
	}
	for i, u := range due {
		if i > 0 {
			t := time.NewTimer(time.Second + rand.N(2*time.Second))
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
		key := "u" + strconv.FormatInt(u.ID, 10)
		if !s.grav.begin(key) {
			continue
		}
		lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		_, err := s.lookupUserGravatar(lctx, u.ID)
		cancel()
		s.grav.end(key)
		if err != nil && ctx.Err() == nil {
			s.log.Warn("gravatar lookup", "user", u.ID, "err", err)
		}
	}
}

// sweepAvatars deletes the avatar files no user or cache row names (an
// address changed on another path, a crash between two writes), an hour
// after they were written.
func (s *Server) sweepAvatars(ctx context.Context, now time.Time) {
	keep, err := s.store.AvatarKeys(ctx)
	if err != nil {
		s.log.Error("sweep avatars", "err", err)
		return
	}
	n, err := s.media.SweepAvatars(ctx, func(key string) bool { return keep[key] }, time.Hour, now)
	if err != nil {
		s.log.Error("sweep avatars", "err", err)
	} else if n > 0 {
		s.log.Info("stray avatar files deleted", "count", n)
	}
}
