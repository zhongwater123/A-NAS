//go:build linux

package workspaceservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/accountremoval"
	"github.com/zhongwater123/A-NAS/internal/modules"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
	"github.com/zhongwater123/A-NAS/internal/storeservice"
	"github.com/zhongwater123/A-NAS/internal/workspace"
	"github.com/zhongwater123/A-NAS/internal/workspaceapi"
)

// UpkeepInterval is how often expired transfer links and old audit events
// are removed.
const UpkeepInterval = 10 * time.Minute

type Config struct {
	Root       string
	SocketPath string
	Directory  Directory
	Logger     *slog.Logger
	// CheckStore accepts Root as the service's store; RequireOwnedStore
	// when nil.
	CheckStore    func(string) error
	RetryInterval time.Duration
	// Admits checks the data volume's reserve; nil uses capacity.Admits.
	Admits func(string, int64) (bool, error)
}

// ErrStoreUnavailable reports a store that is not the service's own on the
// data volume.
var ErrStoreUnavailable = errors.New("workspace store on the data volume is unavailable")

// RequireOwnedStore accepts root only as this service's own private
// directory on the writable, verified data volume, so pages are never
// written to the system disk.
func RequireOwnedStore(root string) error {
	if err := modules.RequireOwnedStore(root); err != nil {
		return fmt.Errorf("%w: %v", ErrStoreUnavailable, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), ".a-nas-volume.json")); err != nil {
		return fmt.Errorf("%w: %v", ErrStoreUnavailable, err)
	}
	return nil
}

// Proxy forwards the workspace to the service on socketPath; while the
// service cannot be reached, requests answer workspace_unavailable.
func Proxy(socketPath string, logger *slog.Logger) http.Handler {
	return serviceproxy.New(socketPath, serviceproxy.Unavailable{
		Code: workspaceapi.Unavailable, Message: "the shared workspace is not available",
	}, logger)
}

// Run serves the workspace on SocketPath until ctx ends. While the store
// cannot be opened, as when the data volume is offline or read-only,
// requests answer workspace_unavailable.
func Run(ctx context.Context, config Config) error {
	if config.Directory == nil {
		return errors.New("the workspace service needs the Host Agent's lookup")
	}
	if config.CheckStore == nil {
		config.CheckStore = RequireOwnedStore
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return storeservice.Run(ctx, storeservice.Config[*workspace.Store]{
		Name: "workspace", Root: config.Root, SocketPath: config.SocketPath, Sessions: config.Directory,
		Unavailable: workspaceapi.Unavailable,
		Open: func(_ context.Context, root string) (*workspace.Store, error) {
			return workspace.Open(root, workspace.Options{Admits: config.Admits})
		},
		API: func(store *workspace.Store) http.Handler {
			return workspaceapi.New(store, workspaceapi.Options{Logger: config.Logger, Directory: config.Directory})
		},
		Remove: RemoveAccount,
		Upkeep: func(ctx context.Context, store *workspace.Store) {
			if err := store.Upkeep(ctx); err != nil && ctx.Err() == nil {
				config.Logger.WarnContext(ctx, "workspace upkeep failed", "error", err)
			}
		},
		UpkeepInterval: UpkeepInterval,
		CheckStore:     config.CheckStore, RetryInterval: config.RetryInterval, Logger: config.Logger,
		Authenticate: func(next http.Handler) http.Handler { return Authenticate(config.Directory, config.Logger, next) },
	})
}

// RemoveAccount forgets a deleted account in store.
func RemoveAccount(store *workspace.Store) accountremoval.Remove {
	return func(ctx context.Context, deletion accountid.AccountDeletion) (accountremoval.Result, error) {
		return accountremoval.Result{}, store.RemoveAccount(ctx, deletion.UserID)
	}
}
