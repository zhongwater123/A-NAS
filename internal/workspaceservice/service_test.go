//go:build linux

package workspaceservice_test

import (
	"errors"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/workspaceservice"
)

func TestTheStoreMustBeTheServicesOwnOnTheDataVolume(t *testing.T) {
	// A temporary directory is not on the Btrfs data volume.
	if err := workspaceservice.RequireOwnedStore(t.TempDir()); !errors.Is(err, workspaceservice.ErrStoreUnavailable) {
		t.Fatalf("RequireOwnedStore(tmp) = %v", err)
	}
}
