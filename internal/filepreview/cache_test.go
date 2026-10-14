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
			cache.entries[key(1)] = diskEntry{size: 4, used: time.Unix(1, 0)}
			if string(cache.get(key(1))) != "1111" {
				t.Fatal("cache miss")
			}
			cache.put(key(3), []byte("3333"))
			if cache.get(key(2)) != nil || cache.bytes != 8 || len(cache.entries) != 2 {
				t.Fatalf("LRU bounds: bytes=%d entries=%d", cache.bytes, len(cache.entries))
			}
			if err := os.WriteFile(filepath.Join(dir, ".preview-interrupted"), []byte("partial"), 0600); err != nil {
				t.Fatal(err)
			}
			restarted, err := openDiskCache(dir)
			if err != nil {
				t.Fatal(err)
			}
			if restarted.bytes != 8 || len(restarted.entries) != 2 || string(restarted.get(key(1))) != "1111" {
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
