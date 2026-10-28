// Package media validates and stores uploads. The file type is detected from
// the content (never from the name or the client's Content-Type), JPEG and
// PNG images are re-encoded to strip metadata such as GPS coordinates, and
// resized variants are generated for responsive images.
package media

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // register WebP decoder for dimensions

	"sap13ns/internal/store"
)

var (
	ErrTooLarge    = errors.New("file too large")
	ErrUnsupported = errors.New("file type not allowed")
	ErrBadImage    = errors.New("image cannot be decoded or is too large")
	// ErrQuota means the site has no room left for the file: a site of a
	// network gets the storage of its plan.
	ErrQuota = errors.New("storage quota exceeded")
)

// A Quota bounds the bytes a site stores. Reserve claims n bytes
// atomically, so concurrent uploads never overshoot, and fails with
// ErrQuota when they do not fit; Release gives bytes back. The network
// measures every site daily, which corrects any drift.
type Quota interface {
	Reserve(ctx context.Context, n int64) error
	Release(ctx context.Context, n int64)
}

// Allowed maps detected MIME types to the extension used on disk. SVG and
// HTML are deliberately absent: they can carry scripts.
var Allowed = map[string]string{
	"image/jpeg":      ".jpg",
	"image/png":       ".png",
	"image/gif":       ".gif",
	"image/webp":      ".webp",
	"image/avif":      ".avif",
	"application/pdf": ".pdf",
	"audio/mpeg":      ".mp3",
	"audio/ogg":       ".ogg",
	"audio/wave":      ".wav",
	"video/mp4":       ".mp4",
	"video/webm":      ".webm",
	"text/plain":      ".txt",
	"font/woff2":      ".woff2", // web fonts for the theme customiser
}

// extraExts lists the other extensions accepted for each detected type.
var extraExts = map[string][]string{
	"image/jpeg": {".jpeg", ".jfif"},
	"audio/wave": {".wave"},
	"text/plain": {".md", ".csv", ".text"},
}

// MatchesName reports whether the original file name's extension fits the
// detected type, so that an SVG or a renamed file is refused instead of being
// stored as something else.
func MatchesName(mime, name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext != "" && (ext == Allowed[mime] || slices.Contains(extraExts[mime], ext))
}

const maxPixels = 50_000_000 // decompression-bomb guard

// Variant widths generated for JPEG and PNG images.
var variantWidths = []struct {
	Name  string
	Width int
}{{"thumb", 480}, {"medium", 1024}, {"large", 1920}}

type Processor struct {
	Dir      string
	MaxBytes int64
	Quota    Quota // nil: no quota (a single-site install)
}

type Result struct {
	Path     string
	MIME     string
	Size     int64
	Width    int
	Height   int
	Variants map[string]store.MediaVariant
	SHA256   string
}

func randomName() string {
	b := make([]byte, 10)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Save stores an upload and returns its metadata. With a Quota, the bytes
// it stores are reserved first: an upload that does not fit fails with
// ErrQuota and leaves no file behind.
func (p *Processor) Save(ctx context.Context, r io.Reader) (*Result, error) {
	tmpDir := filepath.Join(p.Dir, ".tmp")
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(tmpDir, "upload-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r, p.MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if n > p.MaxBytes {
		return nil, ErrTooLarge
	}
	if n == 0 {
		return nil, ErrUnsupported
	}
	head := make([]byte, 512)
	k, err := tmp.ReadAt(head, 0)
	if err != nil && err != io.EOF {
		return nil, err
	}
	mime := Sniff(head[:k]) // a shorter file is not padded with zeros
	ext, ok := Allowed[mime]
	if !ok {
		return nil, ErrUnsupported
	}
	// The upload's size first, so an upload that cannot fit is refused
	// before it is decoded; adjusted below to what was written.
	reserved := int64(0)
	if p.Quota != nil {
		if err := p.Quota.Reserve(ctx, n); err != nil {
			return nil, err
		}
		reserved = n
	}
	res, err := p.store(tmp, mime, ext, n, h)
	if err != nil {
		p.release(ctx, reserved)
		return nil, err
	}
	if p.Quota != nil {
		switch written := p.written(res); {
		case written > reserved:
			if err := p.Quota.Reserve(ctx, written-reserved); err != nil {
				p.removeFiles(res)
				p.release(ctx, reserved)
				return nil, err
			}
		case written < reserved:
			p.release(ctx, reserved-written)
		}
	}
	return res, nil
}

func (p *Processor) release(ctx context.Context, n int64) {
	if p.Quota != nil && n > 0 {
		p.Quota.Release(context.WithoutCancel(ctx), n)
	}
}

// store moves or re-encodes the temporary file of an upload into place.
// On an error it leaves no file behind.
func (p *Processor) store(tmp *os.File, mime, ext string, n int64, h hash.Hash) (*Result, error) {
	now := time.Now().UTC()
	rel := fmt.Sprintf("%04d/%02d", now.Year(), now.Month())
	dir := filepath.Join(p.Dir, rel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	base := randomName()
	res := &Result{MIME: mime, Size: n, SHA256: hex.EncodeToString(h.Sum(nil)), Variants: map[string]store.MediaVariant{}}
	final := filepath.Join(dir, base+ext)

	switch mime {
	case "image/jpeg", "image/png":
		if err := p.processRaster(tmp, mime, dir, rel, base, ext, res); err != nil {
			res.Path = rel + "/" + base + ext
			p.removeFiles(res)
			return nil, err
		}
		fi, err := os.Stat(final)
		if err == nil {
			res.Size = fi.Size()
		}
	default:
		if strings.HasPrefix(mime, "image/") && mime != "image/avif" {
			if _, err := tmp.Seek(0, io.SeekStart); err != nil {
				return nil, err
			}
			cfg, _, err := image.DecodeConfig(bufio.NewReader(tmp))
			if err != nil || cfg.Width*cfg.Height > maxPixels {
				return nil, ErrBadImage
			}
			res.Width, res.Height = cfg.Width, cfg.Height
		}
		if err := tmp.Close(); err != nil {
			return nil, err
		}
		if err := os.Rename(tmp.Name(), final); err != nil {
			return nil, err
		}
		os.Chmod(final, 0o644)
	}
	res.Path = rel + "/" + base + ext
	return res, nil
}

// Sniff detects the type of a file from its first bytes (at most 512 are
// read), never from its name: http.DetectContentType, plus AVIF, without
// parameters ("text/plain", not "text/plain; charset=utf-8").
func Sniff(head []byte) string {
	mime := http.DetectContentType(head)
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = mime[:i]
	}
	// DetectContentType does not know AVIF.
	if len(head) >= 12 && string(head[4:8]) == "ftyp" && (string(head[8:12]) == "avif" || string(head[8:12]) == "avis") {
		return "image/avif"
	}
	if mime == "audio/wav" {
		return "audio/wave"
	}
	if mime == "application/ogg" {
		return "audio/ogg"
	}
	return mime
}

// rasterSlots bounds the images decoded and resized at once (uploads,
// imports, AI images), as avatarSlots does for avatars.
var rasterSlots = make(chan struct{}, 2)

func (p *Processor) processRaster(f *os.File, mime, dir, rel, base, ext string, res *Result) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	cfg, _, err := image.DecodeConfig(bufio.NewReader(f))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxPixels {
		return ErrBadImage
	}
	// A 50-megapixel image decodes to up to 400 MB: a few at once, not a
	// burst, so that parallel uploads cannot exhaust the memory.
	rasterSlots <- struct{}{}
	defer func() { <-rasterSlots }()
	orientation := 1
	if mime == "image/jpeg" {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		orientation = jpegOrientation(f)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	img, _, err := image.Decode(bufio.NewReader(f))
	if err != nil {
		return ErrBadImage
	}
	img = applyOrientation(img, orientation)
	b := img.Bounds()
	res.Width, res.Height = b.Dx(), b.Dy()

	// Re-encoding drops EXIF/XMP/ICC metadata.
	if err := writeImage(filepath.Join(dir, base+ext), img, mime); err != nil {
		return err
	}
	for _, v := range variantWidths {
		if res.Width <= v.Width {
			continue
		}
		h := res.Height * v.Width / res.Width
		dst := image.NewRGBA(image.Rect(0, 0, v.Width, h))
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
		name := base + "-" + v.Name + ext
		if err := writeImage(filepath.Join(dir, name), dst, mime); err != nil {
			return err
		}
		res.Variants[v.Name] = store.MediaVariant{Path: rel + "/" + name, Width: v.Width, Height: h}
	}
	return nil
}

func writeImage(path string, img image.Image, mime string) error {
	var buf bytes.Buffer
	var err error
	switch mime {
	case "image/jpeg":
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 86})
	case "image/png":
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		err = enc.Encode(&buf, img)
	case "image/gif":
		err = gif.Encode(&buf, img, nil)
	}
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Remove deletes the original file and its variants, and gives their
// bytes back to the quota.
func (p *Processor) Remove(ctx context.Context, m *store.Media) {
	var freed int64
	for _, path := range p.files(m.Path, m.Variants) {
		if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() && os.Remove(path) == nil {
			freed += fi.Size()
		}
	}
	p.release(ctx, freed)
}

// files are the paths on disk of a stored file and its variants.
func (p *Processor) files(path string, variants map[string]store.MediaVariant) []string {
	rels := []string{path}
	for _, v := range variants {
		rels = append(rels, v.Path)
	}
	var out []string
	for _, rel := range rels {
		clean := filepath.Clean("/" + rel)[1:]
		if clean == "" || strings.HasPrefix(clean, "..") || strings.HasPrefix(clean, ".tmp") {
			continue
		}
		out = append(out, filepath.Join(p.Dir, clean))
	}
	return out
}

// written adds up the sizes of the files Save stored.
func (p *Processor) written(res *Result) int64 {
	var n int64
	for _, path := range p.files(res.Path, res.Variants) {
		if fi, err := os.Lstat(path); err == nil {
			n += fi.Size()
		}
	}
	return n
}

func (p *Processor) removeFiles(res *Result) {
	for _, path := range p.files(res.Path, res.Variants) {
		os.Remove(path)
	}
}
