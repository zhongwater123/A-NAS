// Package taskicon turns the icon files the Host Agent names for tasks into
// the small PNG or static SVG data URLs that the task manager shows. It runs
// in the Product Service, so that no image decoder runs as root (#221).
package taskicon

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"image"
	_ "image/jpeg"
	"image/png"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp"

	"github.com/zhongwater123/A-NAS/internal/decodepanic"
	"github.com/zhongwater123/A-NAS/internal/taskmanager"
)

// Normalize accepts installed artwork, never arbitrary file content. Raster
// inputs become small PNGs; SVGs must be self-contained static image documents.
// An icon the decoder panics on is dropped, like one it rejects: the same
// file comes back on every refresh.
func Normalize(data []byte) (id, dataURL string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			decodepanic.Log("task icon", recovered)
			id, dataURL = "", ""
		}
	}()
	if len(data) == 0 || len(data) > taskmanager.MaxIconBytes {
		return "", ""
	}
	mime := "image/png"
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("<")) {
		if !taskmanager.StaticSVG(data) {
			return "", ""
		}
		mime = "image/svg+xml"
	} else {
		config, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > 2048 || config.Height > 2048 || config.Width*config.Height > 1024*1024 {
			return "", ""
		}
		original, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return "", ""
		}
		var out bytes.Buffer
		if err := png.Encode(&out, imaging.Fit(original, 64, 64, imaging.Lanczos)); err != nil {
			return "", ""
		}
		data = out.Bytes()
	}
	dataURL = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
	if len(dataURL) > taskmanager.MaxIconDataURL {
		return "", ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), dataURL
}
