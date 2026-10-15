package filepreview

import (
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const diskMaxBytes int64 = 512 << 20
const diskMaxEntries = 4096

type diskEntry struct {
	size int64
	used time.Time
}

// diskCache contains only disposable previews, never sources or source paths.
// Its private directory belongs to the Product Service, outside user shares.
// IO failures are cache misses; a full/unwritable cache must not break browsing.
type diskCache struct {
	mu         sync.Mutex
	directory  string
	entries    map[string]diskEntry
	bytes      int64
	maxBytes   int64
	maxEntries int
}

func openDiskCache(directory string) (*diskCache, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, &os.PathError{Op: "preview cache", Path: directory, Err: os.ErrInvalid}
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	cache := &diskCache{directory: directory, entries: make(map[string]diskEntry), maxBytes: diskMaxBytes, maxEntries: diskMaxEntries}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".preview-") && !entry.IsDir() {
			_ = os.Remove(filepath.Join(directory, entry.Name()))
			continue
		}
		key := strings.TrimSuffix(entry.Name(), ".preview")
		decoded, err := hex.DecodeString(key)
		if err != nil || len(decoded) != 32 || entry.Name() != key+".preview" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 12<<20 {
			_ = os.Remove(filepath.Join(directory, entry.Name()))
			continue
		}
		cache.entries[key] = diskEntry{size: info.Size(), used: info.ModTime()}
		cache.bytes += info.Size()
	}
	cache.trim(0, 0)
	return cache, nil
}

func (c *diskCache) path(key string) string { return filepath.Join(c.directory, key+".preview") }

func (c *diskCache) get(key string) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil
	}
	f, err := os.Open(c.path(key))
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != entry.size {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(f, (12<<20)+1))
	if err != nil || int64(len(data)) != entry.size {
		return nil
	}
	now := time.Now()
	// Only memory misses touch disk recency; ordinary HTTP hits stay in memory.
	_ = os.Chtimes(c.path(key), now, now)
	entry.used = now
	c.entries[key] = entry
	return data
}

func (c *diskCache) put(key string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(data) == 0 || len(data) > 12<<20 {
		return
	}
	if _, ok := c.entries[key]; ok {
		if !c.remove(key) {
			return
		}
	}
	if !c.trim(int64(len(data)), 1) {
		return
	}
	f, err := os.CreateTemp(c.directory, ".preview-")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return
	}
	if err = os.Rename(f.Name(), c.path(key)); err != nil {
		return
	}
	c.entries[key] = diskEntry{size: int64(len(data)), used: time.Now()}
	c.bytes += int64(len(data))
}

func (c *diskCache) remove(key string) bool {
	if err := os.Remove(c.path(key)); err != nil && !os.IsNotExist(err) {
		return false
	}
	c.bytes -= c.entries[key].size
	delete(c.entries, key)
	return true
}

// Called under mu, or during construction. Stop writing if eviction fails;
// continuing would turn a bounded cache into unbounded storage.
func (c *diskCache) trim(extraBytes int64, extraEntries int) bool {
	for c.bytes+extraBytes > c.maxBytes || len(c.entries)+extraEntries > c.maxEntries {
		oldestKey := ""
		var oldest time.Time
		for key, entry := range c.entries {
			if oldestKey == "" || entry.used.Before(oldest) {
				oldestKey, oldest = key, entry.used
			}
		}
		if oldestKey == "" || !c.remove(oldestKey) {
			return false
		}
	}
	return true
}
