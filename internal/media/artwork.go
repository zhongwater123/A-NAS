package media

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"image"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/disintegration/imaging"
	_ "golang.org/x/image/webp"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/decodepanic"
)

// maxArtworkPixels bounds the memory artwork takes to decode twice: a larger
// picture is not decoded, and the decodes in flight together hold at most
// this many pixels (Service.decoding). However many requests arrive, that is
// about 100 MB of JPEG or 425 MB of 16-bit PNG, and about 1 GB at the worst
// for a crafted progressive JPEG; see
// docs/architecture/media-center.md#海报与剧照.
const maxArtworkPixels = 50_000_000

// maxArtworkWaiting bounds the requests waiting for the decode budget, each
// with its picture's file open; more are ErrBusy. A poster wall loads far
// fewer at once.
const maxArtworkWaiting = 256

// errUndecodable is a picture that is not artwork: its header is unreadable
// or out of range, or the decoder rejects or panics on it.
var errUndecodable = errors.New("the picture cannot be decoded as artwork")

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
	defer rows.Close()
	var episodes []*videoRow
	for rows.Next() {
		video, err := scanVideo(rows)
		if err != nil {
			return Image{}, err
		}
		episodes = append(episodes, video)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
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
// cached per file version. A version that cannot be decoded is remembered
// until the service restarts, so a broken picture costs one decode and one
// log record, not one per request.
func (s *Service) resized(ctx context.Context, actor accounts.User, entryID string, size artworkSize) (Image, error) {
	entry, _, err := s.files.Lookup(ctx, actor, entryID)
	if err != nil {
		return Image{}, ErrNotFound
	}
	version := entry.ModifiedAt.UnixNano()
	target := filepath.Join(s.cacheDir, "artwork", fmt.Sprintf("%s--%s-%d.jpg", cacheName(entryID), size.name, version))
	if file, err := os.Open(target); err == nil {
		return Image{File: file, Modified: entry.ModifiedAt}, nil
	}
	undecodable := filepath.Join(s.cacheDir, "artwork", fmt.Sprintf("%s--undecodable-%d", cacheName(entryID), version))
	if _, err := os.Stat(undecodable); err == nil {
		return Image{}, ErrNotFound
	}
	content, err := s.files.OpenContent(ctx, actor, entryID)
	if err != nil {
		return Image{}, err
	}
	defer func() { _ = content.Reader.Close() }()
	scaled, err := s.scaleArtwork(ctx, content.Reader, size)
	if errors.Is(err, errUndecodable) {
		// A marker that cannot be written only means decoding it again.
		_ = writeAtomically(undecodable, nil)
		return Image{}, ErrNotFound
	}
	if err != nil {
		return Image{}, err
	}
	if err := writeAtomically(target, scaled); err != nil {
		return Image{}, err
	}
	file, err := os.Open(target)
	if err != nil {
		return Image{}, err
	}
	return Image{File: file, Modified: entry.ModifiedAt}, nil
}

// scaleArtwork decodes a sidecar image and encodes it as a JPEG that fits
// size, once its pixels are free in the decode budget; waiting ends with the
// request. An image the decoder panics on is undecodable, like one it rejects.
func (s *Service) scaleArtwork(ctx context.Context, r io.ReadSeeker, size artworkSize) (encoded []byte, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			decodepanic.Log("media artwork", recovered)
			encoded, err = nil, errUndecodable
		}
	}()
	config, _, err := image.DecodeConfig(r)
	if err != nil {
		return nil, decodeFailure(err)
	}
	pixels := int64(config.Width) * int64(config.Height)
	if config.Width <= 0 || config.Height <= 0 || pixels > maxArtworkPixels {
		return nil, errUndecodable
	}
	if err := s.waitForPixels(ctx, pixels); err != nil {
		return nil, err
	}
	// Released before the panic above is recovered, too.
	defer s.decoding.Release(pixels)
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	decoded, err := imaging.Decode(r, imaging.AutoOrientation(true))
	if err != nil {
		return nil, decodeFailure(err)
	}
	scaled := decoded
	if decoded.Bounds().Dx() > size.width || decoded.Bounds().Dy() > size.height {
		scaled = imaging.Fit(decoded, size.width, size.height, imaging.Lanczos)
	}
	var buffer bytes.Buffer
	if err := imaging.Encode(&buffer, scaled, imaging.JPEG, imaging.JPEGQuality(85)); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// waitForPixels takes pixels from the decode budget, waiting in turn behind
// the decodes in flight until the request ends.
func (s *Service) waitForPixels(ctx context.Context, pixels int64) error {
	if s.decoding.TryAcquire(pixels) {
		return nil
	}
	if s.decodeWaiting.Add(1) > s.maxDecodeWaiting {
		s.decodeWaiting.Add(-1)
		return ErrBusy
	}
	defer s.decodeWaiting.Add(-1)
	return s.decoding.Acquire(ctx, pixels)
}

// forgetUndecodable removes the undecodable markers of directory, so that
// each start tries such pictures once more: the decoder, or this code, may
// have been fixed since.
func forgetUndecodable(directory string) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "--undecodable-") {
			_ = os.Remove(filepath.Join(directory, entry.Name()))
		}
	}
}

// decodeFailure tells a picture the decoder refuses from a file that could
// not be read, which may succeed later and is not remembered.
func decodeFailure(err error) error {
	if _, ok := errors.AsType[*fs.PathError](err); ok {
		return ErrNotFound
	}
	return errUndecodable
}
