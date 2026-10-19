package media

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"sap13ns/internal/store"
)

// fakeQuota is a site's storage of limit bytes.
type fakeQuota struct {
	mu          sync.Mutex
	used, limit int64
}

func (q *fakeQuota) Reserve(_ context.Context, n int64) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.used+n > q.limit {
		return ErrQuota
	}
	q.used += n
	return nil
}

func (q *fakeQuota) Release(_ context.Context, n int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.used -= n
}

func (q *fakeQuota) get() int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.used
}

// stored adds up the files under dir, outside .tmp, and counts them.
func stored(t *testing.T, dir string) (int, int64) {
	t.Helper()
	var n int
	var size int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.Contains(path, string(filepath.Separator)+".tmp"+string(filepath.Separator)) {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		n++
		size += fi.Size()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n, size
}

func noisyPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewPCG(1, 2))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(r.IntN(256)), uint8(x), uint8(y), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestQuota: what Save stores is reserved, what Remove deletes released,
// and an upload that does not fit leaves no file behind.
func TestQuota(t *testing.T) {
	ctx := context.Background()
	img := noisyPNG(t, 1200, 700)

	q := &fakeQuota{limit: 1 << 30}
	p := &Processor{Dir: t.TempDir(), MaxBytes: 50 << 20, Quota: q}
	res, err := p.Save(ctx, bytes.NewReader(img))
	if err != nil {
		t.Fatal(err)
	}
	files, size := stored(t, p.Dir)
	if files != 3 || len(res.Variants) != 2 || q.get() != size {
		t.Fatalf("%d files of %d bytes, %d variants, %d reserved", files, size, len(res.Variants), q.get())
	}
	p.Remove(ctx, &store.Media{Path: res.Path, Variants: res.Variants})
	if files, _ := stored(t, p.Dir); files != 0 || q.get() != 0 {
		t.Fatalf("after Remove: %d files, %d reserved", files, q.get())
	}

	// Refused before it is decoded.
	q.limit = int64(len(img)) - 1
	if _, err := p.Save(ctx, bytes.NewReader(img)); !errors.Is(err, ErrQuota) {
		t.Fatalf("over the quota: %v", err)
	}
	// It fits, but its variants do not: everything written is removed.
	q.limit = int64(len(img)) + 1
	if _, err := p.Save(ctx, bytes.NewReader(img)); !errors.Is(err, ErrQuota) {
		t.Fatalf("variants over the quota: %v", err)
	}
	if files, _ := stored(t, p.Dir); files != 0 || q.get() != 0 {
		t.Fatalf("after refusals: %d files, %d reserved", files, q.get())
	}

	// A file stored as uploaded is counted at its size.
	q.limit = 1000
	txt := []byte(strings.Repeat("quota ", 100))
	res, err = p.Save(ctx, bytes.NewReader(txt))
	if err != nil || q.get() != int64(len(txt)) || res.MIME != "text/plain" {
		t.Fatalf("text: %+v %v, %d reserved", res, err, q.get())
	}
	if _, err := p.Save(ctx, bytes.NewReader(txt)); !errors.Is(err, ErrQuota) {
		t.Fatalf("a second one: %v", err)
	}
	if files, _ := stored(t, p.Dir); files != 1 {
		t.Fatalf("%d files", files)
	}
	// A file shorter than the sniffed head.
	q.limit = 1 << 20
	if res, err := p.Save(ctx, strings.NewReader("short\n")); err != nil || res.MIME != "text/plain" {
		t.Fatalf("a short file: %+v %v", res, err)
	}

	// Without a quota, nothing is counted.
	free := &Processor{Dir: t.TempDir(), MaxBytes: 50 << 20}
	if _, err := free.Save(ctx, bytes.NewReader(img)); err != nil {
		t.Fatal(err)
	}
}

// TestSaveCleansUp: a bad image leaves nothing behind either.
func TestSaveCleansUp(t *testing.T) {
	q := &fakeQuota{limit: 1 << 30}
	p := &Processor{Dir: t.TempDir(), MaxBytes: 50 << 20, Quota: q}
	bad := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 600)...)
	if _, err := p.Save(context.Background(), bytes.NewReader(bad)); !errors.Is(err, ErrBadImage) {
		t.Fatalf("a bad PNG: %v", err)
	}
	if files, _ := stored(t, p.Dir); files != 0 || q.get() != 0 {
		t.Fatalf("%d files, %d reserved", files, q.get())
	}
	if _, err := os.Stat(filepath.Join(p.Dir, ".tmp")); err != nil {
		t.Fatal(err)
	}
}
