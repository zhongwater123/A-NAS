package main

import (
	"context"
	"testing"
)

type recordingVolumeGuard struct {
	root  string
	write bool
}

func (g *recordingVolumeGuard) Check(_ context.Context, root string, write bool) error {
	g.root = root
	g.write = write
	return nil
}

func TestAppVolumeCheckDoesNotRequireProductServiceWriteAccess(t *testing.T) {
	guard := &recordingVolumeGuard{}
	if err := checkAppVolume(context.Background(), guard, "/srv/a-nas/data"); err != nil {
		t.Fatal(err)
	}
	if guard.root != "/srv/a-nas/data" {
		t.Fatalf("guard root = %q", guard.root)
	}
	if guard.write {
		t.Fatal("app volume check required direct Product Service write access")
	}
}
