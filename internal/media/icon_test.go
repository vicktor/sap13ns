package media

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"testing"
)

// TestMakeIcon: every size is a square PNG, and a wide image is drawn
// whole, letterboxed on transparency, never cropped.
func TestMakeIcon(t *testing.T) {
	var buf bytes.Buffer
	png.Encode(&buf, halves(400, 200))
	files, err := MakeIcon(context.Background(), &buf, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range IconSizes {
		img, format := decodeSize(t, files[size])
		if format != "png" || img.Bounds().Dx() != size || img.Bounds().Dy() != size {
			t.Fatalf("size %d: %s %v", size, format, img.Bounds())
		}
	}
	img, _ := decodeSize(t, files[512])
	if _, _, _, a := img.At(256, 10).RGBA(); a != 0 {
		t.Errorf("the band above a wide image has alpha %d", a)
	}
	if !isRed(img.At(10, 256)) || !isBlue(img.At(500, 256)) {
		t.Errorf("not drawn whole: left %v, right %v", img.At(10, 256), img.At(500, 256))
	}
}

func TestMakeIconRefuses(t *testing.T) {
	for _, data := range [][]byte{
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`),
		pngHeader(5000, 5000),
	} {
		if files, err := MakeIcon(context.Background(), bytes.NewReader(data), 0); err == nil || files != nil {
			t.Errorf("%q: got %d files", data[:8], len(files))
		} else if !errors.Is(err, ErrUnsupported) && !errors.Is(err, ErrBadImage) {
			t.Errorf("%q: %v", data[:8], err)
		}
	}
}
