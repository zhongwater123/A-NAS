// Package filepreview renders ordinary files through the isolated media worker.
// Every request opens the source as its caller, including cache hits.
package filepreview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	ETag        string
}

type cached struct {
	image Image
	used  uint64
}

type Service struct {
	source Source
	worker mediaworker.Worker
	mu     sync.Mutex
	disk   *diskCache
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

// NewWithCache adds a disposable, bounded cache in the product state directory.
func NewWithCache(source Source, worker mediaworker.Worker, directory string) (*Service, error) {
	disk, err := openDiskCache(directory)
	if err != nil {
		return nil, err
	}
	service := New(source, worker)
	service.disk = disk
	return service, nil
}

// Render rechecks source access even on cache hits. Only cache misses wait for
// the decoder; the decode timeout starts after the cancellable queue wait.
func (s *Service) Render(ctx context.Context, actor accounts.User, id string, size int) (Image, error) {
	if size != 320 && size != 1600 {
		return Image{}, mediaworker.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return Image{}, err
	}
	content, key, format, err := s.open(ctx, actor, id, size)
	if err != nil {
		return Image{}, err
	}
	content.Reader.Close()
	if hit, ok := s.lookup(key); ok {
		return hit, nil
	}
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return Image{}, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Reopen after waiting: permissions, source revision, or the file itself may
	// have changed in the queue. Queued requests hold no source descriptors.
	content, key, format, err = s.open(ctx, actor, id, size)
	if err != nil {
		return Image{}, err
	}
	defer content.Reader.Close()
	if hit, ok := s.lookup(key); ok {
		return hit, nil
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
	if !validImage(result) {
		return Image{}, mediaworker.ErrUnreadable
	}
	// A writer can edit an open source while the worker is decoding. Do not
	// associate that result with either revision; the next request retries.
	after, err := content.Reader.Stat()
	if err != nil {
		return Image{}, err
	}
	if revisionKey(actor.ID, id, format, size, after) != key {
		return Image{}, mediaworker.ErrUnreadable
	}
	result.ETag = imageTag(key, result.Data)
	s.remember(key, result)
	if s.disk != nil {
		s.disk.put(key, result.Data)
	}
	return result, nil
}

func (s *Service) open(ctx context.Context, actor accounts.User, id string, size int) (files.Content, string, string, error) {
	content, err := s.source.OpenContent(ctx, actor, id)
	if err != nil {
		return files.Content{}, "", "", err
	}
	format := kind(content.Name)
	info, err := content.Reader.Stat()
	if err == nil && (format == "" || !info.Mode().IsRegular()) {
		err = ErrUnsupported
	}
	key := ""
	if err == nil {
		key = revisionKey(actor.ID, id, format, size, info)
		if key == "" {
			err = ErrUnsupported
		}
	}
	if err != nil {
		content.Reader.Close()
		return files.Content{}, "", "", err
	}
	return content, key, format, nil
}

func revisionKey(actor, id, format string, size int, info os.FileInfo) string {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	// Version the renderer as well as the source. ctime catches SMB writes that
	// preserve size and mtime; account identity prevents cross-user cache reuse.
	identity := fmt.Sprintf("preview-v1/%q/%q/%s/%d/%d/%d/%d/%d/%d", actor, id, format, size, stat.Dev, stat.Ino, stat.Ctim.Nano(), info.ModTime().UnixNano(), info.Size())
	return fmt.Sprintf("%x", sha256.Sum256([]byte(identity)))
}

func imageTag(key string, data []byte) string {
	return fmt.Sprintf("\"%s-%x\"", key, sha256.Sum256(data))
}

func validImage(result Image) bool {
	config, format, err := image.DecodeConfig(bytes.NewReader(result.Data))
	return err == nil && "image/"+format == result.ContentType && len(result.Data) <= 12<<20 && config.Width > 0 && config.Height > 0 && int64(config.Width)*int64(config.Height) <= 16_000_000
}

func (s *Service) lookup(key string) (Image, bool) {
	s.mu.Lock()
	s.clock++
	if hit, ok := s.cache[key]; ok {
		hit.used = s.clock
		s.cache[key] = hit
		s.mu.Unlock()
		return hit.image, true
	}
	s.mu.Unlock()
	if s.disk != nil {
		if data := s.disk.get(key); data != nil {
			_, format, err := image.DecodeConfig(bytes.NewReader(data))
			result := Image{Data: data, ContentType: "image/" + format, ETag: imageTag(key, data)}
			if err == nil && validImage(result) {
				s.remember(key, result)
				return result, true
			}
		}
	}
	return Image{}, false
}

func (s *Service) remember(key string, result Image) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clock++
	if old, ok := s.cache[key]; ok {
		s.bytes -= len(old.image.Data)
		delete(s.cache, key)
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
}
