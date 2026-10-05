package llm

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"testing"
)

func noisePNG(w, h int) []byte {
	r := rand.New(rand.NewSource(1))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = uint8(r.Intn(256))
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func TestShrinkImage_LargePNGBecomesSmallJPEG(t *testing.T) {
	in := noisePNG(2000, 1000)
	if int64(base64.StdEncoding.EncodedLen(len(in))) <= 1_000_000 {
		t.Fatalf("fixture too small: %d", len(in))
	}
	out, mt := ShrinkImage(in, "image/png")
	if mt != "image/jpeg" || len(out) >= len(in) {
		t.Fatalf("mime=%s out=%d in=%d", mt, len(out), len(in))
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != ImageShrinkMaxEdge || b.Dy() != 784 {
		t.Fatalf("bounds %v", b)
	}
}

func TestShrinkImage_SmallUnchanged(t *testing.T) {
	in := noisePNG(50, 50)
	out, mt := ShrinkImage(in, "image/png")
	if mt != "image/png" || !bytes.Equal(in, out) {
		t.Fatal("small image changed")
	}
}

func TestShrinkImage_PassThrough(t *testing.T) {
	old := ImageShrinkThreshold
	defer func() { ImageShrinkThreshold = old }()
	ImageShrinkThreshold = 10
	junk := bytes.Repeat([]byte("x"), 100)
	for _, m := range []string{"image/webp", "image/png", "image/jpeg", "image/gif"} {
		out, mt := ShrinkImage(junk, m) // corrupt or unsupported: no panic, unchanged
		if mt != m || !bytes.Equal(out, junk) {
			t.Fatalf("%s changed", m)
		}
	}
	big := noisePNG(100, 100)
	ImageShrinkThreshold = 0
	if out, _ := ShrinkImage(big, "image/png"); !bytes.Equal(out, big) {
		t.Fatal("opt-out ignored")
	}
}

func TestShrinkImage_TruncatedPNGKept(t *testing.T) {
	old := ImageShrinkThreshold
	defer func() { ImageShrinkThreshold = old }()
	ImageShrinkThreshold = 10
	in := noisePNG(100, 100)
	in = in[:len(in)/2]
	if out, mt := ShrinkImage(in, "image/png"); mt != "image/png" || !bytes.Equal(out, in) {
		t.Fatal("truncated image altered")
	}
}

func TestOrientRotates(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	o := orient(img, 6) // 90 CW: top-left goes to top-right
	if b := o.Bounds(); b.Dx() != 2 || b.Dy() != 4 {
		t.Fatalf("bounds %v", b)
	}
	if r, _, _, _ := o.At(1, 0).RGBA(); r == 0 {
		t.Fatal("pixel not moved")
	}
}

func TestExifOrientationParse(t *testing.T) {
	tiff := []byte{'M', 'M', 0, 42, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, 6, 0, 0}
	seg := append([]byte("Exif\x00\x00"), tiff...)
	n := len(seg) + 2
	data := append([]byte{0xFF, 0xD8, 0xFF, 0xE1, byte(n >> 8), byte(n)}, seg...)
	if got := jpegOrientation(data); got != 6 {
		t.Fatalf("got %d", got)
	}
}

// An image whose raw size is under the threshold but whose base64 size is
// over it must be shrunk: the request body carries the base64 form.
func TestShrinkImage_UsesEncodedSize(t *testing.T) {
	old := ImageShrinkThreshold
	defer func() { ImageShrinkThreshold = old }()
	ImageShrinkThreshold = 1_000_000
	var in []byte
	for w := 300; w < 1400; w += 10 {
		in = noisePNG(w, 300)
		if len(in) > 800_000 {
			break
		}
	}
	enc := int64(base64.StdEncoding.EncodedLen(len(in)))
	if len(in) >= 1_000_000 || enc <= 1_000_000 {
		t.Fatalf("fixture raw=%d enc=%d", len(in), enc)
	}
	out, mt := ShrinkImage(in, "image/png")
	if mt != "image/jpeg" || len(out) >= len(in) {
		t.Fatalf("not shrunk: mime=%s out=%d in=%d", mt, len(out), len(in))
	}
	b64, _, _ := ShrinkBase64Image(base64.StdEncoding.EncodeToString(in), "image/png")
	if len(b64) >= 1_000_000 {
		t.Fatalf("base64 payload still %d", len(b64))
	}
}

func TestDefaultShrinkThresholdEncodedBudget(t *testing.T) {
	t.Setenv("RAKITSU_IMAGE_SHRINK_BYTES", "")
	if got := defaultImageShrinkThreshold(); got != 1_000_000 {
		t.Fatalf("default %d", got)
	}
}
