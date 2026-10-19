package media

// Avatars: profile photos, uploaded by their owner or copied from
// Gravatar. Whatever the source, the image is decoded and drawn again:
// centre-cropped to a square, scaled to each of AvatarSizes and encoded as
// JPEG, or PNG when it has transparency. Nothing of the original file
// survives but its pixels: no EXIF (GPS included), XMP, ICC profile or
// comment, and no payload hidden after the image data.

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/draw"
)

const (
	// AvatarMaxBytes is the largest photo accepted.
	AvatarMaxBytes = 5 << 20
	// AvatarMaxPixels bounds the decoded image (a decompression bomb
	// guard; a 20 megapixel RGBA image takes 80 MB while it is drawn).
	AvatarMaxPixels = 20_000_000
	// AvatarDir is the directory of avatars in the uploads.
	AvatarDir = "avatars"
)

// AvatarSizes are the square sizes, in pixels, an avatar is stored in.
var AvatarSizes = []int{64, 128, 256}

// avatarFormats are the types an avatar is made from, by their sniffed
// MIME type, with the name the image package gives their decoder. AVIF
// is missing: the binary has no AVIF decoder.
var avatarFormats = map[string]string{"image/jpeg": "jpeg", "image/png": "png", "image/webp": "webp", "image/gif": "gif"}

// AvatarTypes are the MIME types MakeAvatar accepts.
func AvatarTypes() []string { return []string{"image/jpeg", "image/png", "image/webp", "image/gif"} }

// An Avatar is a photo ready to store: one encoded file per size.
type Avatar struct {
	Ext   string         // ".jpg" or ".png"
	Files map[int][]byte // by size, for each of AvatarSizes
}

// Bytes is what the avatar takes on disk.
func (a *Avatar) Bytes() int64 {
	var n int64
	for _, b := range a.Files {
		n += int64(len(b))
	}
	return n
}

// avatarSlots bounds the avatars decoded at once, and with them the memory
// a burst of uploads takes.
var avatarSlots = make(chan struct{}, 2)

// MakeAvatar reads an image of at most maxBytes (AvatarMaxBytes when 0)
// from r and makes its avatar. The type is sniffed from the bytes, never
// taken from a name or a header: JPEG, PNG, WebP or GIF (its first frame).
// It fails with ErrTooLarge, ErrUnsupported (another type, AVIF included)
// or ErrBadImage (a file that does not decode, or more than
// AvatarMaxPixels).
func MakeAvatar(ctx context.Context, r io.Reader, maxBytes int64) (*Avatar, error) {
	if maxBytes <= 0 {
		maxBytes = AvatarMaxBytes
	}
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrTooLarge
	}
	format, ok := avatarFormats[Sniff(data)]
	if !ok {
		return nil, ErrUnsupported
	}
	cfg, got, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || got != format || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > AvatarMaxPixels {
		return nil, ErrBadImage
	}
	select {
	case avatarSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-avatarSlots }()
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrBadImage
	}
	big := square(img, AvatarSizes[len(AvatarSizes)-1])
	// The centre square of a rotated photo is the rotated centre square,
	// so the EXIF orientation is applied to the small image only.
	if format == "jpeg" {
		if o := jpegOrientation(bytes.NewReader(data)); o > 1 {
			big = toRGBA(applyOrientation(big, o))
		}
	}
	a := &Avatar{Ext: ".jpg", Files: map[int][]byte{}}
	if !opaque(big) {
		a.Ext = ".png"
	}
	for _, size := range AvatarSizes {
		im := big
		if size != big.Bounds().Dx() {
			im = image.NewRGBA(image.Rect(0, 0, size, size))
			draw.CatmullRom.Scale(im, im.Bounds(), big, big.Bounds(), draw.Src, nil)
		}
		var buf bytes.Buffer
		if a.Ext == ".png" {
			err = (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, im)
		} else {
			err = jpeg.Encode(&buf, im, &jpeg.Options{Quality: 85})
		}
		if err != nil {
			return nil, err
		}
		a.Files[size] = buf.Bytes()
	}
	return a, nil
}

// square draws the centre square of img at size × size.
func square(img image.Image, size int) *image.RGBA {
	b := img.Bounds()
	side := min(b.Dx(), b.Dy())
	x0, y0 := b.Min.X+(b.Dx()-side)/2, b.Min.Y+(b.Dy()-side)/2
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, image.Rect(x0, y0, x0+side, y0+side), draw.Src, nil)
	return dst
}

func toRGBA(img image.Image) *image.RGBA {
	if m, ok := img.(*image.RGBA); ok {
		return m
	}
	dst := image.NewRGBA(img.Bounds())
	draw.Draw(dst, dst.Bounds(), img, img.Bounds().Min, draw.Src)
	return dst
}

func opaque(m *image.RGBA) bool {
	for i := 3; i < len(m.Pix); i += 4 {
		if m.Pix[i] != 0xff {
			return false
		}
	}
	return true
}

// avatarKey is the name of a stored avatar: avatars/<owner>-<random>.<ext>,
// where the owner is u<id> (a user's photo), g<id> (a user's Gravatar) or
// c (a commenter's Gravatar). Its files add the size before the extension.
var avatarKey = regexp.MustCompile(`^avatars/(u[0-9]{1,19}|g[0-9]{1,19}|c)-[0-9a-f]{20}\.(jpg|png)$`)

// avatarOwner is the owner part of a key (avatarKey).
var avatarOwner = regexp.MustCompile(`^(u[0-9]{1,19}|g[0-9]{1,19}|c)$`)

// avatarFile is the name of one file of an avatar.
var avatarFile = regexp.MustCompile(`^((?:u[0-9]{1,19}|g[0-9]{1,19}|c)-[0-9a-f]{20})-([0-9]{2,3})\.(jpg|png)$`)

// ValidAvatarKey reports whether key names a stored avatar.
func ValidAvatarKey(key string) bool { return avatarKey.MatchString(key) }

// AvatarFile is the path, relative to the uploads, of the file of the
// avatar key at size pixels.
func AvatarFile(key string, size int) string {
	ext := path.Ext(key)
	return strings.TrimSuffix(key, ext) + "-" + strconv.Itoa(size) + ext
}

// SaveAvatar stores a in the uploads as owner-<random> (see avatarKey) and
// returns its key. With a Quota, its bytes are reserved first: an avatar
// that does not fit fails with ErrQuota and leaves no file behind.
func (p *Processor) SaveAvatar(ctx context.Context, a *Avatar, owner string) (string, error) {
	if !avatarOwner.MatchString(owner) || (a.Ext != ".jpg" && a.Ext != ".png") || len(a.Files) != len(AvatarSizes) {
		return "", errors.New("media: invalid avatar")
	}
	n := a.Bytes()
	if p.Quota != nil {
		if err := p.Quota.Reserve(ctx, n); err != nil {
			return "", err
		}
	}
	key := AvatarDir + "/" + owner + "-" + randomName() + a.Ext
	err := os.MkdirAll(filepath.Join(p.Dir, AvatarDir), 0o755)
	for _, size := range AvatarSizes {
		if err != nil {
			break
		}
		b, ok := a.Files[size]
		if !ok {
			err = errors.New("media: avatar size missing")
			break
		}
		err = writeFile(filepath.Join(p.Dir, filepath.FromSlash(AvatarFile(key, size))), b)
	}
	if err != nil {
		for _, size := range AvatarSizes {
			os.Remove(filepath.Join(p.Dir, filepath.FromSlash(AvatarFile(key, size))))
		}
		p.release(ctx, n)
		return "", err
	}
	return key, nil
}

func writeFile(name string, b []byte) error {
	tmp := name + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, name); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// RemoveAvatar deletes the files of the avatar key, and gives their bytes
// back to the quota. Anything but an avatar's key is ignored.
func (p *Processor) RemoveAvatar(ctx context.Context, key string) {
	if !ValidAvatarKey(key) {
		return
	}
	var freed int64
	for _, size := range AvatarSizes {
		name := filepath.Join(p.Dir, filepath.FromSlash(AvatarFile(key, size)))
		if fi, err := os.Lstat(name); err == nil && fi.Mode().IsRegular() && os.Remove(name) == nil {
			freed += fi.Size()
		}
	}
	p.release(ctx, freed)
}

// SweepAvatars deletes the avatar files whose key keep does not report
// and that are older than grace (an avatar being saved has its files
// before its key is recorded), and gives their bytes back. It returns the
// number of files deleted.
func (p *Processor) SweepAvatars(ctx context.Context, keep func(key string) bool, grace time.Duration, now time.Time) (int, error) {
	entries, err := os.ReadDir(filepath.Join(p.Dir, AvatarDir))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var (
		freed int64
		n     int
	)
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			break
		}
		name := e.Name()
		key := ""
		if m := avatarFile.FindStringSubmatch(name); m != nil {
			key = AvatarDir + "/" + m[1] + "." + m[3]
		}
		if key != "" && keep(key) {
			continue
		}
		fi, err := e.Info()
		if err != nil || !fi.Mode().IsRegular() || now.Sub(fi.ModTime()) < grace {
			continue
		}
		if os.Remove(filepath.Join(p.Dir, AvatarDir, name)) == nil {
			freed += fi.Size()
			n++
		}
	}
	p.release(ctx, freed)
	return n, nil
}
