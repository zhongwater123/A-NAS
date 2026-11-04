package filepreview

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDiskCacheBoundsAndRestartCleanup(t *testing.T) {
	for _, limit := range []string{"bytes", "entries"} {
		t.Run(limit, func(t *testing.T) {
			dir := t.TempDir()
			cache, err := openDiskCache(dir)
			if err != nil {
				t.Fatal(err)
			}
			cache.maxBytes, cache.maxEntries = 8, 10
			if limit == "entries" {
				cache.maxBytes, cache.maxEntries = 100, 2
			}
			key := func(i int) string { return fmt.Sprintf("%064x", i) }
			cache.put(key(1), []byte("1111"))
			cache.put(key(2), []byte("2222"))
			// Reading the older of the two makes the other the least recently used.
			if string(cache.get(key(1))) != "1111" {
				t.Fatal("cache miss")
			}
			cache.put(key(3), []byte("3333"))
			if cache.get(key(2)) != nil || cache.bytes != 8 || cache.entries.Len() != 2 {
				t.Fatalf("LRU bounds: bytes=%d entries=%d", cache.bytes, cache.entries.Len())
			}
			if err := os.WriteFile(filepath.Join(dir, ".preview-interrupted"), []byte("partial"), 0600); err != nil {
				t.Fatal(err)
			}
			restarted, err := openDiskCache(dir)
			if err != nil {
				t.Fatal(err)
			}
			if restarted.bytes != 8 || restarted.entries.Len() != 2 || string(restarted.get(key(1))) != "1111" {
				t.Fatal("restart did not restore cache")
			}
			if _, err := os.Stat(filepath.Join(dir, ".preview-interrupted")); !os.IsNotExist(err) {
				t.Fatal("interrupted write remained")
			}
			info, _ := os.Stat(dir)
			if info.Mode().Perm() != 0700 {
				t.Fatalf("directory mode=%v", info.Mode())
			}
		})
	}
}

// A restart orders the previews by when they were last used, their
// modification time, so the next eviction still takes the stalest.
func TestDiskCacheEvictsTheLeastRecentlyUsedAfterARestart(t *testing.T) {
	dir := t.TempDir()
	cache, err := openDiskCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	key := func(i int) string { return fmt.Sprintf("%064x", i) }
	for i := 1; i <= 3; i++ {
		cache.put(key(i), []byte("data"))
	}
	for i, used := range map[int]int64{1: 300, 2: 200, 3: 100} {
		when := time.Unix(used, 0)
		if err := os.Chtimes(cache.path(key(i)), when, when); err != nil {
			t.Fatal(err)
		}
	}
	restarted, err := openDiskCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	restarted.maxEntries = 3
	restarted.put(key(4), []byte("data"))
	if restarted.get(key(3)) != nil || restarted.get(key(2)) == nil || restarted.get(key(1)) == nil {
		t.Fatal("a restarted cache evicted another preview than the least recently used")
	}
}
