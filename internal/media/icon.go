package media

// Site icons: the image a site shows in browser tabs, bookmarks and on
// the home screen of phones. Like avatars, the image is decoded and drawn
// again, so nothing of the original file survives but its pixels; unlike
// avatars it is never cropped: the whole image is centred on a
// transparent square, and every size is a PNG.

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"

	"golang.org/x/image/draw"
)

// IconSizes are the square sizes, in pixels, a site icon is served in:
// the browser tab (32), the Apple touch icon (180) and the web app
// manifest (192 and 512). The largest comes first.
var IconSizes = []int{512, 192, 180, 32}

// IconTypes are the MIME types MakeIcon accepts.
func IconTypes() []string { return AvatarTypes() }

// MakeIcon reads an image of at most maxBytes (AvatarMaxBytes when 0) from
// r and draws it whole, centred on a transparent square, as one PNG per
// size in IconSizes. The type is sniffed from the bytes: JPEG, PNG, WebP
// or GIF. It fails like MakeAvatar.
func MakeIcon(ctx context.Context, r io.Reader, maxBytes int64) (map[int][]byte, error) {
	img, format, data, release, err := decodeSmall(ctx, r, maxBytes)
	if err != nil {
		return nil, err
	}
	defer release()
	if format == "jpeg" {
		if o := jpegOrientation(bytes.NewReader(data)); o > 1 {
			img = applyOrientation(img, o)
		}
	}
	big := contain(img, IconSizes[0])
	files := make(map[int][]byte, len(IconSizes))
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	for _, size := range IconSizes {
		im := big
		if size != big.Bounds().Dx() {
			im = image.NewRGBA(image.Rect(0, 0, size, size))
			draw.CatmullRom.Scale(im, im.Bounds(), big, big.Bounds(), draw.Src, nil)
		}
		var buf bytes.Buffer
		if err := enc.Encode(&buf, im); err != nil {
			return nil, err
		}
		files[size] = buf.Bytes()
	}
	return files, nil
}

// contain draws img whole, keeping its proportions, in the middle of a
// transparent size × size square.
func contain(img image.Image, size int) *image.RGBA {
	b := img.Bounds()
	w, h := size, size
	if b.Dx() > b.Dy() {
		h = max(1, b.Dy()*size/b.Dx())
	} else if b.Dy() > b.Dx() {
		w = max(1, b.Dx()*size/b.Dy())
	}
	x0, y0 := (size-w)/2, (size-h)/2
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.CatmullRom.Scale(dst, image.Rect(x0, y0, x0+w, y0+h), img, b, draw.Over, nil)
	return dst
}
