package sitebackup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/media"
	"sap13ns/internal/store"
	"sap13ns/internal/testutil"
)

// TestPhotosRoundTrip: a backup holds the users' uploaded photos and not
// the Gravatar copies; a restore gives each user of the backup their
// photo again, encoded anew under their id on the site, keeps the photo
// of the site's own users, and forgets every Gravatar lookup.
func TestPhotosRoundTrip(t *testing.T) {
	pool := testutil.Schemas(t)
	ctx := context.Background()
	a := testutil.SiteStore(t, pool, "site_1")
	b := testutil.SiteStore(t, pool, "site_2")
	base := t.TempDir()
	upA, upB := filepath.Join(base, "sites/1/uploads"), filepath.Join(base, "sites/2/uploads")
	dirA, dirB := filepath.Join(base, "backups/sites/1"), filepath.Join(base, "backups/sites/2")
	s := seed(t, a, upA)

	avatar := func(w, h int) *media.Avatar {
		t.Helper()
		av, err := media.MakeAvatar(ctx, bytes.NewReader(pngBytes(t, w, h)), 0)
		if err != nil {
			t.Fatal(err)
		}
		return av
	}
	save := func(p *media.Processor, owner string) string {
		t.Helper()
		key, err := p.SaveAvatar(ctx, avatar(300, 200), owner)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	pa := &media.Processor{Dir: upA}
	photo := save(pa, fmt.Sprintf("u%d", s.author))
	if _, err := a.SetUserAvatar(ctx, s.author, photo); err != nil {
		t.Fatal(err)
	}
	gcopy := save(pa, fmt.Sprintf("g%d", s.editor))
	if _, ok, err := a.SetUserGravatar(ctx, s.editor, "edith@example.com", gcopy); !ok || err != nil {
		t.Fatal(ok, err)
	}
	ccopy := save(pa, "c")
	if _, err := a.PutCommenterGravatar(ctx, strings.Repeat("ab", 32), ccopy, 100); err != nil {
		t.Fatal(err)
	}

	arch, _, err := Create(ctx, a, siteOpts(dirA, upA, 1))
	if err != nil {
		t.Fatal(err)
	}
	files := members(t, arch.Path)
	for _, size := range media.AvatarSizes {
		if files[DirUploads+media.AvatarFile(photo, size)] == nil {
			t.Errorf("the backup lacks the photo at %d px", size)
		}
	}
	for name := range files {
		if strings.HasPrefix(name, DirUploads+"avatars/g") || strings.HasPrefix(name, DirUploads+"avatars/c-") {
			t.Errorf("the backup holds the Gravatar copy %s", name)
		}
	}
	var users []sideUser
	if err := json.Unmarshal(files[nameUsers], &users); err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		if want := map[bool]string{true: photo}[u.ID == s.author]; u.Avatar != want {
			t.Errorf("%s: avatar %q, want %q", u.Username, u.Avatar, want)
		}
	}

	// The other site has an administrator with a photo of their own.
	zed, err := b.CreateUser(ctx, store.NewUser{Username: "zed", Email: "zed@example.com", Role: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	pb := &media.Processor{Dir: upB}
	zedPhoto := save(pb, fmt.Sprintf("u%d", zed))
	if _, err := b.SetUserAvatar(ctx, zed, zedPhoto); err != nil {
		t.Fatal(err)
	}

	rep, err := Restore(ctx, b, arch.Path, restoreOpts(siteOpts(dirB, upB, 2), zed, true))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Photos != 1 {
		t.Errorf("%d photos restored; warnings %v", rep.Photos, rep.Warnings)
	}
	arthur, err := b.UserByUsername(ctx, "arthur")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(arthur.Avatar, fmt.Sprintf("avatars/u%d-", arthur.ID)) || arthur.Avatar == photo {
		t.Fatalf("restored photo %q (was %q)", arthur.Avatar, photo)
	}
	for _, size := range media.AvatarSizes {
		data, err := os.ReadFile(filepath.Join(upB, filepath.FromSlash(media.AvatarFile(arthur.Avatar, size))))
		if err != nil {
			t.Fatal(err)
		}
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err != nil || cfg.Width != size || cfg.Height != size {
			t.Errorf("size %d: %dx%d %v", size, cfg.Width, cfg.Height, err)
		}
		if _, err := os.Stat(filepath.Join(upB, filepath.FromSlash(media.AvatarFile(zedPhoto, size)))); err != nil {
			t.Errorf("zed's photo is gone: %v", err)
		}
	}
	if u, _ := b.UserByUsername(ctx, "zed"); u.Avatar != zedPhoto {
		t.Errorf("zed's photo: %q", u.Avatar)
	}
	edith, _ := b.UserByUsername(ctx, "edith")
	if edith.Gravatar != "" {
		t.Errorf("a Gravatar copy was restored: %q", edith.Gravatar)
	}
	due, err := b.GravatarDue(ctx, time.Now(), 100)
	if err != nil {
		t.Fatal(err)
	}
	looked := false
	for _, u := range due {
		looked = looked || u.ID == edith.ID
	}
	if !looked {
		t.Error("edith is not looked up again")
	}
	if cached, err := b.CommenterGravatars(ctx, []string{strings.Repeat("ab", 32)}); err != nil || len(cached) != 0 {
		t.Errorf("the commenters' cache was restored: %v %v", cached, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(upB, media.AvatarDir)); len(entries) != 2*len(media.AvatarSizes) {
		t.Errorf("%d files in the avatars directory, want %d", len(entries), 2*len(media.AvatarSizes))
	}
}
