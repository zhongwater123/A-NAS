// Package filepreview renders ordinary files through the isolated media worker.
// Every request opens the source as its caller, including cache hits.
package filepreview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/mediaworker"
)

var ErrUnsupported = errors.New("file has no rendered preview")

type Source interface {
	OpenContent(context.Context, accounts.User, string) (files.Content, error)
}

type Image struct {
	Data        []byte
	ContentType string
}

type cached struct {
	image Image
	used  uint64
}

type Service struct {
	source Source
	worker mediaworker.Worker
	gate   chan struct{}
	cache  map[string]cached
	bytes  int
	clock  uint64
}

func New(source Source, worker mediaworker.Worker) *Service {
	return &Service{source: source, worker: worker, gate: make(chan struct{}, 1), cache: make(map[string]cached)}
}

func kind(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".heic", ".heif":
		return "heif"
	case ".mp4", ".mov", ".mkv", ".webm", ".avi", ".m4v", ".mpg", ".mpeg", ".mts", ".m2ts", ".flv", ".f4v", ".wmv", ".3gp":
		return "video"
	}
	return ""
}

// Render keeps at most one decode in flight and 32 MiB / 64 previews in memory.
// No filenames, image data or decoded output are written into users' folders.
func (s *Service) Render(ctx context.Context, actor accounts.User, id string, size int) (Image, error) {
	if size != 320 && size != 1600 {
		return Image{}, mediaworker.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return Image{}, ctx.Err()
	}
	content, err := s.source.OpenContent(ctx, actor, id)
	if err != nil {
		return Image{}, err
	}
	defer content.Reader.Close()
	format := kind(content.Name)
	if format == "" {
		return Image{}, ErrUnsupported
	}
	info, err := content.Reader.Stat()
	if err != nil {
		return Image{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() {
		return Image{}, ErrUnsupported
	}
	// fstat catches SMB edits even before the next directory listing. ctime
	// also detects replacements or edits that preserve size and mtime.
	key := fmt.Sprintf("%s/%s/%s/%d/%d/%d/%d/%d", actor.ID, id, format, size, stat.Dev, stat.Ino, stat.Ctim.Nano(), info.Size())
	s.clock++
	if hit, ok := s.cache[key]; ok {
		hit.used = s.clock
		s.cache[key] = hit
		return hit.image, nil
	}
	if s.worker == nil {
		return Image{}, mediaworker.ErrUnavailable
	}
	var result Image
	if format == "heif" {
		result.ContentType = "image/png"
		result.Data, err = s.worker.Image(ctx, content.Reader, size)
	} else {
		result.ContentType = "image/jpeg"
		var probe mediaworker.Probe
		probe, err = s.worker.Probe(ctx, content.Reader)
		if err == nil {
			at := math.Min(probe.Format.Duration/10, 10)
			if math.IsNaN(at) || math.IsInf(at, 0) || at < 0 {
				at = 0
			}
			result.Data, err = s.worker.Frame(ctx, content.Reader, at, size)
		}
	}
	if err != nil {
		return Image{}, err
	}
	config, decodedFormat, decodeErr := image.DecodeConfig(bytes.NewReader(result.Data))
	if decodeErr != nil || "image/"+decodedFormat != result.ContentType || len(result.Data) > 12<<20 || config.Width <= 0 || config.Height <= 0 || config.Width*config.Height > 16_000_000 {
		return Image{}, mediaworker.ErrUnreadable
	}
	for len(s.cache) >= 64 || s.bytes+len(result.Data) > 32<<20 {
		oldestKey := ""
		oldest := s.clock
		for key, item := range s.cache {
			if item.used < oldest {
				oldestKey, oldest = key, item.used
			}
		}
		s.bytes -= len(s.cache[oldestKey].image.Data)
		delete(s.cache, oldestKey)
	}
	s.cache[key] = cached{image: result, used: s.clock}
	s.bytes += len(result.Data)
	return result, nil
}
