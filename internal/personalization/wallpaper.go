package personalization

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"slices"
	"time"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp"

	"github.com/zhongwater123/A-NAS/internal/decodepanic"
)

// The ways the desktop can draw the wallpaper.
const (
	StyleFill    = "fill"
	StyleFit     = "fit"
	StyleStretch = "stretch"
	StyleCenter  = "center"
	StyleTile    = "tile"
)

var WallpaperStyles = []string{StyleFill, StyleFit, StyleStretch, StyleCenter, StyleTile}

const (
	// MaxWallpaperUploadBytes bounds the picture an administrator may send.
	MaxWallpaperUploadBytes = 24 << 20
	// MaxWallpaperEdge is the longest side kept: sharp on a 4K screen.
	MaxWallpaperEdge = 3840
	// WallpaperContentType is the type of every stored wallpaper.
	WallpaperContentType = "image/jpeg"
	// maxWallpaperPixels bounds decoded dimensions before any allocation; a
	// 25-megapixel picture decodes to about 100 MB. Pages send at most
	// 3840 px on the long side.
	maxWallpaperPixels = 25_000_000
)

var (
	ErrInvalidStyle     = errors.New("style must be fill, fit, stretch, center or tile")
	ErrUnsupportedImage = errors.New("the picture is not a JPEG, PNG, GIF or WebP image")
	ErrImageTooLarge    = errors.New("the picture is too large")
	ErrNoWallpaperImage = errors.New("the default wallpaper is in use")
)

// backdrop is the navy of the default wallpaper. It fills transparent pixels
// here, and the desktop shows it wherever a picture does not reach.
var backdrop = color.NRGBA{R: 0x03, G: 0x11, B: 0x27, A: 0xff}

// Wallpaper is what every desktop draws. Without Image it is the built-in
// default wallpaper, drawn in Style all the same.
type Wallpaper struct {
	Style string          `json:"style"`
	Image *WallpaperImage `json:"image,omitempty"`
}

type WallpaperImage struct {
	// Version names one picture for good, so browsers keep it cached.
	Version string `json:"version"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
}

// WallpaperPicture is the stored picture with what serving it needs.
type WallpaperPicture struct {
	Data      []byte
	Version   string
	UpdatedAt time.Time
}

// NormalizeWallpaper decodes picture, turns it upright, scales it down to at
// most MaxWallpaperEdge on the long side and fills transparency with the
// backdrop. The result is a JPEG without the original's metadata, whatever was
// uploaded, so no client decides what other people's desktops load. A picture
// the decoder panics on is unsupported, like one it rejects.
func NormalizeWallpaper(picture []byte) (data []byte, width, height int, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			decodepanic.Log("wallpaper", recovered)
			data, width, height, err = nil, 0, 0, ErrUnsupportedImage
		}
	}()
	if len(picture) > MaxWallpaperUploadBytes {
		return nil, 0, 0, ErrImageTooLarge
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(picture))
	if err != nil {
		return nil, 0, 0, ErrUnsupportedImage
	}
	switch format {
	case "jpeg", "png", "gif", "webp":
	default:
		return nil, 0, 0, ErrUnsupportedImage
	}
	if config.Width <= 0 || config.Height <= 0 {
		return nil, 0, 0, ErrUnsupportedImage
	}
	if config.Width*config.Height > maxWallpaperPixels {
		return nil, 0, 0, ErrImageTooLarge
	}
	source, err := imaging.Decode(bytes.NewReader(picture), imaging.AutoOrientation(true))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("%w: %v", ErrUnsupportedImage, err)
	}
	if bounds := source.Bounds(); bounds.Dx() > MaxWallpaperEdge || bounds.Dy() > MaxWallpaperEdge {
		source = imaging.Fit(source, MaxWallpaperEdge, MaxWallpaperEdge, imaging.Lanczos)
	}
	bounds := source.Bounds()
	flat := imaging.Overlay(imaging.New(bounds.Dx(), bounds.Dy(), backdrop), source, image.Point{}, 1)
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, flat, &jpeg.Options{Quality: 90}); err != nil {
		return nil, 0, 0, err
	}
	return encoded.Bytes(), bounds.Dx(), bounds.Dy(), nil
}

func (s *Store) Wallpaper(ctx context.Context) (Wallpaper, error) {
	wallpaper := Wallpaper{Style: StyleFill}
	var stored WallpaperImage
	err := s.db.QueryRowContext(ctx, "SELECT style, version, width, height FROM wallpaper WHERE id = 1").
		Scan(&wallpaper.Style, &stored.Version, &stored.Width, &stored.Height)
	if errors.Is(err, sql.ErrNoRows) {
		return wallpaper, nil
	}
	if err != nil {
		return Wallpaper{}, err
	}
	if stored.Version != "" {
		wallpaper.Image = &stored
	}
	return wallpaper, nil
}

func (s *Store) SetWallpaperStyle(ctx context.Context, style string) (Wallpaper, error) {
	if !slices.Contains(WallpaperStyles, style) {
		return Wallpaper{}, ErrInvalidStyle
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO wallpaper(id, style) VALUES(1, ?)
ON CONFLICT(id) DO UPDATE SET style = excluded.style`, style)
	if err != nil {
		return Wallpaper{}, err
	}
	return s.Wallpaper(ctx)
}

// SetWallpaperImage replaces the picture with one NormalizeWallpaper made,
// keeping the style.
func (s *Store) SetWallpaperImage(ctx context.Context, data []byte, width, height int) (Wallpaper, error) {
	digest := sha256.Sum256(data)
	version := hex.EncodeToString(digest[:8])
	_, err := s.db.ExecContext(ctx, `INSERT INTO wallpaper(id, style, image, version, width, height, updated_at) VALUES(1, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET image = excluded.image, version = excluded.version, width = excluded.width,
    height = excluded.height, updated_at = excluded.updated_at`,
		StyleFill, data, version, width, height, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return Wallpaper{}, err
	}
	return s.Wallpaper(ctx)
}

// ResetWallpaper goes back to the built-in wallpaper, filled.
func (s *Store) ResetWallpaper(ctx context.Context) (Wallpaper, error) {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM wallpaper"); err != nil {
		return Wallpaper{}, err
	}
	return s.Wallpaper(ctx)
}

func (s *Store) WallpaperPicture(ctx context.Context) (WallpaperPicture, error) {
	var picture WallpaperPicture
	var updatedAt string
	err := s.db.QueryRowContext(ctx, "SELECT image, version, updated_at FROM wallpaper WHERE id = 1 AND version != ''").
		Scan(&picture.Data, &picture.Version, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return WallpaperPicture{}, ErrNoWallpaperImage
	}
	if err != nil {
		return WallpaperPicture{}, err
	}
	picture.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return picture, nil
}
