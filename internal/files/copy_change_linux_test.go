package files

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type changingCopyContext struct {
	context.Context
	calls  int
	change func()
}

func (c *changingCopyContext) Err() error {
	c.calls++
	if c.calls == 3 {
		c.change()
	}
	return nil
}
func TestCopyRejectsSourceChangedBetweenChunks(t *testing.T) {
	root := t.TempDir()
	source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
	f, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(copyChunk + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := &changingCopyContext{Context: context.Background(), change: func() {
		// Models SMB modifying an already copied part of the source.
		file, err := os.OpenFile(source, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteAt([]byte("new bytes"), 0); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		later := time.Now().Add(time.Minute)
		if err := os.Chtimes(source, later, later); err != nil {
			t.Fatal(err)
		}
	}}
	if err := NewRootFileSystem(root, true).Copy(ctx, source, target); !errors.Is(err, ErrConflict) {
		t.Fatalf("copy of changing file: %v", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial target retained: %v", err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatal(err)
	}
}
