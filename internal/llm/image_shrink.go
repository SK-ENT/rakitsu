package llm

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"strconv"
)

// ImageShrinkThreshold is the encoded size above which an image is
// downscaled and re-encoded as JPEG before it is sent to a provider.
// 0 or less disables shrinking. Override with RAKITSU_IMAGE_SHRINK_BYTES
// (set it to 0 to opt out). A var so tests can change it.
var ImageShrinkThreshold int64 = defaultImageShrinkThreshold()

const (
	// ImageShrinkMaxEdge is the longest edge, in pixels, of a shrunk image.
	ImageShrinkMaxEdge = 1568
	// imageShrinkMaxPixels bounds decode memory (decode-bomb guard). Larger
	// images are sent as they are.
	imageShrinkMaxPixels = 100_000_000
	imageShrinkQuality   = 85
)

func defaultImageShrinkThreshold() int64 {
	if v := os.Getenv("RAKITSU_IMAGE_SHRINK_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return 1 << 20
}

// ShrinkImage returns data unchanged unless it is a png/jpeg/gif larger than
// ImageShrinkThreshold. Then it downscales to ImageShrinkMaxEdge and
// re-encodes as JPEG (animated gifs keep only the first frame). On any
// failure, or if the result is not smaller, the original is returned.
func ShrinkImage(data []byte, mimeType string) ([]byte, string) {
	if ImageShrinkThreshold <= 0 || int64(len(data)) <= ImageShrinkThreshold {
		return data, mimeType
	}
	var cfg image.Config
	var err error
	switch mimeType {
	case "image/png":
		cfg, err = png.DecodeConfig(bytes.NewReader(data))
	case "image/jpeg":
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(data))
	case "image/gif":
		cfg, err = gif.DecodeConfig(bytes.NewReader(data))
	default:
		return data, mimeType
	}
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > imageShrinkMaxPixels {
		return data, mimeType
	}
	out, ok := shrink(data, mimeType)
	if !ok || len(out) >= len(data) {
		return data, mimeType
	}
	return out, "image/jpeg"
}

func shrink(data []byte, mimeType string) (out []byte, ok bool) {
	defer func() {
		if recover() != nil {
			out, ok = nil, false
		}
	}()
	var img image.Image
	var err error
	switch mimeType {
	case "image/png":
		img, err = png.Decode(bytes.NewReader(data))
	case "image/jpeg":
		img, err = jpeg.Decode(bytes.NewReader(data))
		if err == nil {
			img = orient(img, jpegOrientation(data))
		}
	case "image/gif":
		img, err = gif.Decode(bytes.NewReader(data))
	}
	if err != nil {
		return nil, false
	}
	img = boxDownscale(img, ImageShrinkMaxEdge)
	// Flatten alpha on white: JPEG has no transparency.
	b := img.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Over)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, rgba, &jpeg.Options{Quality: imageShrinkQuality}); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

// boxDownscale shrinks img so its longest edge is at most maxEdge, averaging
// the source pixels that fall in each destination pixel.
func boxDownscale(img image.Image, maxEdge int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxEdge && h <= maxEdge {
		return img
	}
	nw, nh := maxEdge, maxEdge
	if w >= h {
		nh = max(1, h*maxEdge/w)
	} else {
		nw = max(1, w*maxEdge/h)
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		y0, y1 := y*h/nh, max(y*h/nh+1, (y+1)*h/nh)
		for x := 0; x < nw; x++ {
			x0, x1 := x*w/nw, max(x*w/nw+1, (x+1)*w/nw)
			var r, g, bl, a, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, ca := img.At(b.Min.X+sx, b.Min.Y+sy).RGBA()
					r, g, bl, a, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca), n+1
				}
			}
			// Premultiplied 16-bit sums back to 8-bit premultiplied.
			dst.SetRGBA(x, y, color.RGBA{uint8(r / n >> 8), uint8(g / n >> 8), uint8(bl / n >> 8), uint8(a / n >> 8)})
		}
	}
	return dst
}

// jpegOrientation returns the EXIF orientation (1-8) of a JPEG, or 1.
func jpegOrientation(data []byte) int {
	i := 2
	for i+4 <= len(data) && data[i] == 0xFF {
		marker := data[i+1]
		if marker == 0xDA || marker == 0xD9 {
			break
		}
		n := int(binary.BigEndian.Uint16(data[i+2:]))
		if n < 2 || i+2+n > len(data) {
			break
		}
		seg := data[i+4 : i+2+n]
		if marker == 0xE1 && len(seg) >= 14 && string(seg[:6]) == "Exif\x00\x00" {
			return exifOrientation(seg[6:])
		}
		i += 2 + n
	}
	return 1
}

func exifOrientation(t []byte) int {
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	off := int(bo.Uint32(t[4:]))
	if off < 8 || off+2 > len(t) {
		return 1
	}
	cnt := int(bo.Uint16(t[off:]))
	for k := 0; k < cnt; k++ {
		e := off + 2 + k*12
		if e+12 > len(t) {
			break
		}
		if bo.Uint16(t[e:]) == 0x0112 {
			if v := int(bo.Uint16(t[e+8:])); v >= 1 && v <= 8 {
				return v
			}
		}
	}
	return 1
}

// orient applies an EXIF orientation so re-encoding (which drops EXIF) keeps
// the picture upright.
func orient(img image.Image, o int) image.Image {
	if o <= 1 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	nw, nh := w, h
	if o >= 5 {
		nw, nh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
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
			dst.Set(dx, dy, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

// shrinkBase64Image is ShrinkImage for a base64 payload. Invalid base64 or
// no shrink returns the inputs unchanged.
func shrinkBase64Image(b64, mimeType string) (string, string, int64) {
	if ImageShrinkThreshold <= 0 || int64(base64.StdEncoding.DecodedLen(len(b64))) <= ImageShrinkThreshold {
		return b64, mimeType, int64(base64.StdEncoding.DecodedLen(len(b64)))
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return b64, mimeType, int64(base64.StdEncoding.DecodedLen(len(b64)))
	}
	out, m := ShrinkImage(raw, mimeType)
	if len(out) == len(raw) && m == mimeType {
		return b64, mimeType, int64(len(raw))
	}
	return base64.StdEncoding.EncodeToString(out), m, int64(len(out))
}

// ShrinkBase64Image shrinks a base64 image payload (see ShrinkImage) and
// returns the new payload, MIME type and decoded size.
func ShrinkBase64Image(b64, mimeType string) (string, string, int64) {
	return shrinkBase64Image(b64, mimeType)
}
