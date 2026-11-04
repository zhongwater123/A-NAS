package filepreview

import (
	"encoding/hex"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2/simplelru"
)

const diskMaxBytes int64 = 512 << 20
const diskMaxEntries = 4096

// diskCache contains only disposable previews, never sources or source paths.
// Its private directory belongs to the Product Service, outside user shares.
// IO failures are cache misses; a full/unwritable cache must not break browsing.
type diskCache struct {
	mu        sync.Mutex
	directory string
	// entries holds each preview's size, least recently used first. Only
	// trim evicts, never the LRU itself: a file must be gone before its entry.
	entries    *simplelru.LRU[string, int64]
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
	index, err := simplelru.NewLRU[string, int64](math.MaxInt, nil)
	if err != nil {
		return nil, err
	}
	cache := &diskCache{directory: directory, entries: index, maxBytes: diskMaxBytes, maxEntries: diskMaxEntries}
	type found struct {
		key  string
		size int64
		used time.Time
	}
	var previews []found
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
		previews = append(previews, found{key: key, size: info.Size(), used: info.ModTime()})
	}
	// A preview's modification time is when it was last used.
	slices.SortFunc(previews, func(a, b found) int { return a.used.Compare(b.used) })
	for _, preview := range previews {
		cache.entries.Add(preview.key, preview.size)
		cache.bytes += preview.size
	}
	cache.trim(0, 0)
	return cache, nil
}

func (c *diskCache) path(key string) string { return filepath.Join(c.directory, key+".preview") }

func (c *diskCache) get(key string) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	size, ok := c.entries.Peek(key)
	if !ok {
		return nil
	}
	f, err := os.Open(c.path(key))
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(f, (12<<20)+1))
	if err != nil || int64(len(data)) != size {
		return nil
	}
	now := time.Now()
	// Only memory misses touch disk recency; ordinary HTTP hits stay in memory.
	_ = os.Chtimes(c.path(key), now, now)
	c.entries.Get(key)
	return data
}

func (c *diskCache) put(key string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(data) == 0 || len(data) > 12<<20 {
		return
	}
	if c.entries.Contains(key) {
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
	defer func() { _ = os.Remove(f.Name()) }()
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return
	}
	if err = os.Rename(f.Name(), c.path(key)); err != nil {
		return
	}
	c.entries.Add(key, int64(len(data)))
	c.bytes += int64(len(data))
}

func (c *diskCache) remove(key string) bool {
	if err := os.Remove(c.path(key)); err != nil && !os.IsNotExist(err) {
		return false
	}
	size, _ := c.entries.Peek(key)
	c.bytes -= size
	c.entries.Remove(key)
	return true
}

// Called under mu, or during construction. Stop writing if eviction fails;
// continuing would turn a bounded cache into unbounded storage.
func (c *diskCache) trim(extraBytes int64, extraEntries int) bool {
	for c.bytes+extraBytes > c.maxBytes || c.entries.Len()+extraEntries > c.maxEntries {
		oldest, _, ok := c.entries.GetOldest()
		if !ok || !c.remove(oldest) {
			return false
		}
	}
	return true
}
