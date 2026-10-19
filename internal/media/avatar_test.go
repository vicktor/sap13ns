package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// halves is a w × h image, red on the left half and blue on the right.
func halves(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := color.RGBA{220, 20, 20, 255}
			if x >= w/2 {
				c = color.RGBA{20, 20, 220, 255}
			}
			img.Set(x, y, c)
		}
	}
	return img
}

// exifJPEG encodes img as a JPEG with an EXIF segment (orientation, and a
// description standing for GPS data) and a comment segment.
func exifJPEG(t *testing.T, img image.Image, orientation uint16, secret string) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	plain := buf.Bytes()

	// TIFF, little endian: an IFD of two entries, the orientation and an
	// ImageDescription pointing past the IFD.
	desc := append([]byte(secret), 0)
	tiff := []byte("II*\x00")
	tiff = binary.LittleEndian.AppendUint32(tiff, 8)
	tiff = binary.LittleEndian.AppendUint16(tiff, 2)
	tiff = binary.LittleEndian.AppendUint16(tiff, 0x0112) // Orientation
	tiff = binary.LittleEndian.AppendUint16(tiff, 3)      // SHORT
	tiff = binary.LittleEndian.AppendUint32(tiff, 1)
	tiff = binary.LittleEndian.AppendUint16(tiff, orientation)
	tiff = binary.LittleEndian.AppendUint16(tiff, 0)
	tiff = binary.LittleEndian.AppendUint16(tiff, 0x010E) // ImageDescription
	tiff = binary.LittleEndian.AppendUint16(tiff, 2)      // ASCII
	tiff = binary.LittleEndian.AppendUint32(tiff, uint32(len(desc)))
	tiff = binary.LittleEndian.AppendUint32(tiff, uint32(8+2+2*12+4))
	tiff = binary.LittleEndian.AppendUint32(tiff, 0) // no next IFD
	tiff = append(tiff, desc...)
	app1 := append([]byte("Exif\x00\x00"), tiff...)
	com := []byte(secret + " comment")

	out := append([]byte{}, plain[:2]...) // SOI
	out = append(out, 0xFF, 0xE1)
	out = binary.BigEndian.AppendUint16(out, uint16(len(app1)+2))
	out = append(out, app1...)
	out = append(out, 0xFF, 0xFE)
	out = binary.BigEndian.AppendUint16(out, uint16(len(com)+2))
	out = append(out, com...)
	out = append(out, plain[2:]...)
	if jpegOrientation(bytes.NewReader(out)) != int(orientation) {
		t.Fatal("the test JPEG has no EXIF orientation")
	}
	return out
}

func decodeSize(t *testing.T, b []byte) (image.Image, string) {
	t.Helper()
	img, format, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img, format
}

func isRed(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return r > 0x9000 && g < 0x6000 && b < 0x6000
}

func isBlue(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return b > 0x9000 && r < 0x6000 && g < 0x6000
}

// TestMakeAvatarStripsMetadata: a JPEG with EXIF (orientation, a GPS-like
// description) and a comment becomes three square JPEGs that hold none of
// it, turned as the orientation says.
func TestMakeAvatarStripsMetadata(t *testing.T) {
	const secret = "GPSLatitude 40.4168 N"
	data := exifJPEG(t, halves(600, 400), 6, secret)
	a, err := MakeAvatar(context.Background(), bytes.NewReader(data), 0)
	if err != nil {
		t.Fatal(err)
	}
	if a.Ext != ".jpg" || len(a.Files) != len(AvatarSizes) {
		t.Fatalf("ext %q, %d files", a.Ext, len(a.Files))
	}
	for _, size := range AvatarSizes {
		b := a.Files[size]
		img, format := decodeSize(t, b)
		if format != "jpeg" || img.Bounds().Dx() != size || img.Bounds().Dy() != size {
			t.Fatalf("size %d: %s %v", size, format, img.Bounds())
		}
		for _, bad := range [][]byte{[]byte("Exif"), []byte(secret), []byte("comment"), {0xFF, 0xE1}, {0xFF, 0xFE}} {
			if bytes.Contains(b, bad) {
				t.Errorf("size %d still holds %q", size, bad)
			}
		}
		if o := jpegOrientation(bytes.NewReader(b)); o != 1 {
			t.Errorf("size %d: orientation %d", size, o)
		}
	}
	// Orientation 6 turns the photo a quarter clockwise: the left half
	// (red) is now on top.
	img, _ := decodeSize(t, a.Files[256])
	if top, bottom := img.At(128, 20), img.At(128, 235); !isRed(top) || !isBlue(bottom) {
		t.Errorf("not turned: top %v, bottom %v", top, bottom)
	}
	if a.Bytes() <= 0 {
		t.Error("no bytes")
	}
}

// TestMakeAvatarCrops: the centre square is kept.
func TestMakeAvatarCrops(t *testing.T) {
	var buf bytes.Buffer
	png.Encode(&buf, halves(300, 900))
	a, err := MakeAvatar(context.Background(), &buf, 0)
	if err != nil {
		t.Fatal(err)
	}
	img, format := decodeSize(t, a.Files[256])
	if format != "jpeg" {
		t.Fatalf("an opaque PNG gave %s", format)
	}
	if !isRed(img.At(20, 128)) || !isBlue(img.At(235, 128)) {
		t.Errorf("crop: left %v, right %v", img.At(20, 128), img.At(235, 128))
	}
}

// TestMakeAvatarTransparency: a PNG with transparency stays a PNG, and
// stays transparent.
func TestMakeAvatarTransparency(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 100, 100))
	for y := range 100 {
		for x := range 50 {
			img.Set(x, y, color.NRGBA{0, 200, 0, 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	a, err := MakeAvatar(context.Background(), &buf, 0)
	if err != nil {
		t.Fatal(err)
	}
	if a.Ext != ".png" {
		t.Fatalf("ext %q", a.Ext)
	}
	out, format := decodeSize(t, a.Files[64])
	if format != "png" {
		t.Fatal(format)
	}
	if _, _, _, alpha := out.At(60, 30).RGBA(); alpha != 0 {
		t.Errorf("the transparent half has alpha %d", alpha)
	}
}

// pngHeader is a PNG that only has a header saying it is w × h.
func pngHeader(w, h uint32) []byte {
	ihdr := binary.BigEndian.AppendUint32(nil, w)
	ihdr = binary.BigEndian.AppendUint32(ihdr, h)
	ihdr = append(ihdr, 8, 2, 0, 0, 0)
	chunk := append([]byte("IHDR"), ihdr...)
	out := []byte("\x89PNG\r\n\x1a\n")
	out = binary.BigEndian.AppendUint32(out, uint32(len(ihdr)))
	out = append(out, chunk...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(chunk))
}

// TestMakeAvatarRefuses: what is not a photo it can make, whatever its
// name would say.
func TestMakeAvatarRefuses(t *testing.T) {
	var small bytes.Buffer
	png.Encode(&small, halves(40, 40))
	jpg := exifJPEG(t, halves(64, 64), 1, "x")
	for _, c := range []struct {
		name string
		data []byte
		max  int64
		want error
	}{
		{"text named .jpg", []byte("I am not a photo, whatever my name says.\n"), 0, ErrUnsupported},
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), 0, ErrUnsupported},
		{"html", []byte("<!DOCTYPE html><html><body>hi</body></html>"), 0, ErrUnsupported},
		{"avif", append([]byte("\x00\x00\x00\x1cftypavif\x00\x00\x00\x00avifmif1miaf"), make([]byte, 64)...), 0, ErrUnsupported},
		{"pdf", []byte("%PDF-1.7\n%âãÏÓ\n"), 0, ErrUnsupported},
		{"truncated jpeg", jpg[:len(jpg)/3], 0, ErrBadImage},
		{"too many pixels", pngHeader(5000, 5000), 0, ErrBadImage},
		{"no pixels", pngHeader(0, 10), 0, ErrBadImage},
		{"too large", small.Bytes(), int64(small.Len() - 1), ErrTooLarge},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, err := MakeAvatar(context.Background(), bytes.NewReader(c.data), c.max)
			if !errors.Is(err, c.want) || a != nil {
				t.Fatalf("got %v, %v; want %v", a, err, c.want)
			}
		})
	}
	if Sniff([]byte("\x00\x00\x00\x1cftypavif")) != "image/avif" {
		t.Error("the AVIF case does not sniff as AVIF")
	}
}

func avatarFixture(t *testing.T) *Avatar {
	t.Helper()
	var buf bytes.Buffer
	png.Encode(&buf, halves(120, 120))
	a, err := MakeAvatar(context.Background(), &buf, 0)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestSaveRemoveAvatar: the files are reserved against the quota and
// named randomly; removing them gives the bytes back; one that does not
// fit leaves nothing.
func TestSaveRemoveAvatar(t *testing.T) {
	ctx := context.Background()
	a := avatarFixture(t)
	q := &fakeQuota{limit: 10 << 20}
	p := &Processor{Dir: t.TempDir(), Quota: q}
	key, err := p.SaveAvatar(ctx, a, "u7")
	if err != nil {
		t.Fatal(err)
	}
	if !ValidAvatarKey(key) || !strings.HasPrefix(key, "avatars/u7-") {
		t.Fatalf("key %q", key)
	}
	other, err := p.SaveAvatar(ctx, a, "u7")
	if err != nil || other == key {
		t.Fatalf("a second photo: %q, %v", other, err)
	}
	if q.get() != 2*a.Bytes() {
		t.Fatalf("reserved %d, want %d", q.get(), 2*a.Bytes())
	}
	for _, size := range AvatarSizes {
		b, err := os.ReadFile(filepath.Join(p.Dir, AvatarFile(key, size)))
		if err != nil || !bytes.Equal(b, a.Files[size]) {
			t.Fatalf("size %d: %v", size, err)
		}
	}
	p.RemoveAvatar(ctx, key)
	p.RemoveAvatar(ctx, other)
	p.RemoveAvatar(ctx, "../etc/passwd") // ignored
	if n, _ := stored(t, p.Dir); n != 0 || q.get() != 0 {
		t.Fatalf("%d files left, %d bytes reserved", n, q.get())
	}

	q.limit = a.Bytes() - 1
	if _, err := p.SaveAvatar(ctx, a, "u7"); !errors.Is(err, ErrQuota) {
		t.Fatalf("over the quota: %v", err)
	}
	if n, _ := stored(t, p.Dir); n != 0 || q.get() != 0 {
		t.Fatalf("over the quota: %d files, %d bytes", n, q.get())
	}
	for _, owner := range []string{"", "x", "u", "u1/../x", "c1"} {
		if _, err := p.SaveAvatar(ctx, a, owner); err == nil {
			t.Errorf("owner %q accepted", owner)
		}
	}
}

// TestSweepAvatars: files no key keeps go once older than the grace, and
// give their bytes back; kept and recent ones stay.
func TestSweepAvatars(t *testing.T) {
	ctx := context.Background()
	a := avatarFixture(t)
	q := &fakeQuota{limit: 10 << 20}
	p := &Processor{Dir: t.TempDir(), Quota: q}
	kept, _ := p.SaveAvatar(ctx, a, "u1")
	stray, _ := p.SaveAvatar(ctx, a, "g2")
	junk := filepath.Join(p.Dir, AvatarDir, "notes.txt")
	os.WriteFile(junk, []byte("x"), 0o644)
	keep := func(k string) bool { return k == kept }

	if n, err := p.SweepAvatars(ctx, keep, time.Hour, time.Now()); err != nil || n != 0 {
		t.Fatalf("within the grace: %d, %v", n, err)
	}
	n, err := p.SweepAvatars(ctx, keep, time.Hour, time.Now().Add(2*time.Hour))
	if err != nil || n != len(AvatarSizes)+1 {
		t.Fatalf("swept %d, %v", n, err)
	}
	for _, size := range AvatarSizes {
		if _, err := os.Stat(filepath.Join(p.Dir, AvatarFile(kept, size))); err != nil {
			t.Error(err)
		}
		if _, err := os.Stat(filepath.Join(p.Dir, AvatarFile(stray, size))); !os.IsNotExist(err) {
			t.Errorf("stray %d: %v", size, err)
		}
	}
	// The stray photo's bytes and the junk's (counted by the measurement)
	// are given back.
	if q.get() != a.Bytes()-1 {
		t.Errorf("reserved %d, want %d", q.get(), a.Bytes()-1)
	}
}

func TestAvatarKeys(t *testing.T) {
	for key, ok := range map[string]bool{
		"avatars/u12-0123456789abcdef0123.jpg":   true,
		"avatars/g3-0123456789abcdef0123.png":    true,
		"avatars/c-0123456789abcdef0123.jpg":     true,
		"avatars/u12-0123456789abcdef0123.gif":   false,
		"avatars/x-0123456789abcdef0123.jpg":     false,
		"avatars/c1-0123456789abcdef0123.jpg":    false,
		"avatars/u12-0123456789abcdef012.jpg":    false,
		"../avatars/u1-0123456789abcdef0123.jpg": false,
		"2026/10/photo.jpg":                      false,
		"":                                       false,
	} {
		if ValidAvatarKey(key) != ok {
			t.Errorf("%q: %v", key, !ok)
		}
	}
	if got := AvatarFile("avatars/u12-0123456789abcdef0123.jpg", 64); got != "avatars/u12-0123456789abcdef0123-64.jpg" {
		t.Error(got)
	}
}
