package media

import (
	"bufio"
	"encoding/binary"
	"image"
	"io"
)

// jpegOrientation reads the EXIF orientation tag (1-8) from a JPEG stream.
// It only parses the APP1 segment headers it needs and returns 1 on any error.
func jpegOrientation(r io.Reader) int {
	br := bufio.NewReader(r)
	var soi [2]byte
	if _, err := io.ReadFull(br, soi[:]); err != nil || soi[0] != 0xFF || soi[1] != 0xD8 {
		return 1
	}
	for i := 0; i < 32; i++ {
		var marker [4]byte
		if _, err := io.ReadFull(br, marker[:]); err != nil || marker[0] != 0xFF {
			return 1
		}
		size := int(binary.BigEndian.Uint16(marker[2:])) - 2
		if size < 0 || size > 1<<16 {
			return 1
		}
		if marker[1] == 0xDA { // start of scan: no EXIF before image data
			return 1
		}
		seg := make([]byte, size)
		if _, err := io.ReadFull(br, seg); err != nil {
			return 1
		}
		if marker[1] == 0xE1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			return parseOrientation(seg[6:])
		}
	}
	return 1
}

func parseOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	off := int(bo.Uint32(tiff[4:8]))
	if off < 8 || off+2 > len(tiff) {
		return 1
	}
	n := int(bo.Uint16(tiff[off:]))
	for i := 0; i < n; i++ {
		e := off + 2 + i*12
		if e+12 > len(tiff) {
			return 1
		}
		if bo.Uint16(tiff[e:]) == 0x0112 {
			v := int(bo.Uint16(tiff[e+8:]))
			if v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// applyOrientation rotates/flips an image according to its EXIF orientation
// so that the re-encoded file (without EXIF) displays correctly.
func applyOrientation(img image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	var dst *image.RGBA
	if o >= 5 {
		dst = image.NewRGBA(image.Rect(0, 0, h, w))
	} else {
		dst = image.NewRGBA(image.Rect(0, 0, w, h))
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := img.At(b.Min.X+x, b.Min.Y+y)
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, c)
		}
	}
	return dst
}
