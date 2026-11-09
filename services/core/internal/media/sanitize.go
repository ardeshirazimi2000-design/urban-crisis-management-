package media

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
)

// MaxPixels bounds decoding work (and memory) per uploaded image.
const MaxPixels = 40_000_000

var errTooLarge = errors.New("image dimensions too large")

// Sanitize decodes and re-encodes JPEG and PNG images (content disarm and reconstruction): anything that is
// not pixels — appended payloads, polyglot content, EXIF with the device's location — does not survive. It
// returns ok=false for types it cannot rebuild (WebP, video), which keep the scanner's verdict.
func Sanitize(data []byte, contentType string) (out []byte, ok bool, err error) {
	if contentType != "image/jpeg" && contentType != "image/png" {
		return data, false, nil
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, false, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > MaxPixels {
		return nil, false, errTooLarge
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false, err
	}
	var buf bytes.Buffer
	if contentType == "image/png" {
		err = png.Encode(&buf, img)
	} else {
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85})
	}
	if err != nil {
		return nil, false, err
	}
	return buf.Bytes(), true, nil
}
