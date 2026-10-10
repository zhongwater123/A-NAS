package media

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// maxArtworkPixels refuses images that would take too much memory to decode.
const maxArtworkPixels = 50_000_000

type artworkSize struct {
	name          string
	width, height int
}

var (
	posterSize   = artworkSize{"poster", 480, 720}
	backdropSize = artworkSize{"backdrop", 1280, 720}
	thumbSize    = artworkSize{"thumb", 640, 360}
)

// Image is a cached JPEG; the caller closes File.
type Image struct {
	File     *os.File
	Modified time.Time
}

// Artwork returns a title's poster, backdrop or thumb: sidecar images from
// the library resized once, or a frame from the video.
func (s *Service) Artwork(ctx context.Context, actor accounts.User, id, kind string) (Image, error) {
	if s.cacheDir == "" {
		return Image{}, ErrNotFound
	}
	switch {
	case strings.HasPrefix(id, "video:"):
		video, _, err := s.visibleVideo(ctx, actor, id)
		if err != nil {
			return Image{}, err
		}
		return s.videoArtwork(ctx, actor, video, kind)
	case strings.HasPrefix(id, "show:"):
		var show showRow
		err := s.store.db.QueryRowContext(ctx, "SELECT id, library_id, poster_entry, backdrop_entry FROM shows WHERE id = ?", id).
			Scan(&show.ID, &show.LibraryID, &show.Poster, &show.Backdrop)
		if errors.Is(err, sql.ErrNoRows) {
			return Image{}, ErrNotFound
		}
		if err != nil {
			return Image{}, err
		}
		if _, err := s.library(ctx, actor, show.LibraryID); err != nil {
			return Image{}, err
		}
		return s.showArtwork(ctx, actor, show, kind)
	}
	return Image{}, ErrNotFound
}

func (s *Service) videoArtwork(ctx context.Context, actor accounts.User, video *videoRow, kind string) (Image, error) {
	switch kind {
	case "poster":
		if video.Poster != "" {
			return s.resized(ctx, actor, video.Poster, posterSize)
		}
	case "backdrop":
		if video.Backdrop != "" {
			return s.resized(ctx, actor, video.Backdrop, backdropSize)
		}
		if video.hasFrame() {
			return s.frame(video.ID)
		}
		if video.Thumb != "" {
			return s.resized(ctx, actor, video.Thumb, backdropSize)
		}
	case "thumb":
		if video.Thumb != "" {
			return s.resized(ctx, actor, video.Thumb, thumbSize)
		}
		if video.hasFrame() {
			return s.frame(video.ID)
		}
		if video.Backdrop != "" {
			return s.resized(ctx, actor, video.Backdrop, thumbSize)
		}
	default:
		return Image{}, ErrInvalid
	}
	return Image{}, ErrNotFound
}

func (s *Service) showArtwork(ctx context.Context, actor accounts.User, show showRow, kind string) (Image, error) {
	if kind == "poster" {
		if show.Poster == "" {
			return Image{}, ErrNotFound
		}
		return s.resized(ctx, actor, show.Poster, posterSize)
	}
	if kind != "backdrop" && kind != "thumb" {
		return Image{}, ErrInvalid
	}
	if kind == "backdrop" && show.Backdrop != "" {
		return s.resized(ctx, actor, show.Backdrop, backdropSize)
	}
	rows, err := s.store.db.QueryContext(ctx, "SELECT "+videoColumns+" FROM videos WHERE show_id = ?", show.ID)
	if err != nil {
		return Image{}, err
	}
	var episodes []*videoRow
	for rows.Next() {
		video, err := scanVideo(rows)
		if err != nil {
			_ = rows.Close()
			return Image{}, err
		}
		episodes = append(episodes, video)
	}
	if err := rows.Close(); err != nil {
		return Image{}, err
	}
	sortEpisodes(episodes)
	still := (&showRow{episodes: episodes}).still()
	if still != nil {
		return s.videoArtwork(ctx, actor, still, "thumb")
	}
	if show.Backdrop != "" {
		return s.resized(ctx, actor, show.Backdrop, thumbSize)
	}
	return Image{}, ErrNotFound
}

func (s *Service) frame(videoID string) (Image, error) {
	file, err := os.Open(s.framePath(videoID))
	if errors.Is(err, os.ErrNotExist) {
		return Image{}, ErrNotFound
	}
	if err != nil {
		return Image{}, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return Image{}, err
	}
	return Image{File: file, Modified: info.ModTime()}, nil
}

// resized returns a sidecar image scaled to fit size, read as the actor and
// cached per file version.
func (s *Service) resized(ctx context.Context, actor accounts.User, entryID string, size artworkSize) (Image, error) {
	entry, _, err := s.files.Lookup(ctx, actor, entryID)
	if err != nil {
		return Image{}, ErrNotFound
	}
	target := filepath.Join(s.cacheDir, "artwork", fmt.Sprintf("%s--%s-%d.jpg", cacheName(entryID), size.name, entry.ModifiedAt.UnixNano()))
	if file, err := os.Open(target); err == nil {
		return Image{File: file, Modified: entry.ModifiedAt}, nil
	}
	content, err := s.files.OpenContent(ctx, actor, entryID)
	if err != nil {
		return Image{}, err
	}
	defer content.Reader.Close()
	config, _, err := image.DecodeConfig(content.Reader)
	if err != nil || config.Width*config.Height > maxArtworkPixels {
		return Image{}, ErrNotFound
	}
	if _, err := content.Reader.Seek(0, io.SeekStart); err != nil {
		return Image{}, err
	}
	decoded, err := imaging.Decode(content.Reader, imaging.AutoOrientation(true))
	if err != nil {
		return Image{}, ErrNotFound
	}
	scaled := decoded
	if decoded.Bounds().Dx() > size.width || decoded.Bounds().Dy() > size.height {
		scaled = imaging.Fit(decoded, size.width, size.height, imaging.Lanczos)
	}
	var buffer bytes.Buffer
	if err := imaging.Encode(&buffer, scaled, imaging.JPEG, imaging.JPEGQuality(85)); err != nil {
		return Image{}, err
	}
	if err := writeAtomically(target, buffer.Bytes()); err != nil {
		return Image{}, err
	}
	file, err := os.Open(target)
	if err != nil {
		return Image{}, err
	}
	return Image{File: file, Modified: entry.ModifiedAt}, nil
}
