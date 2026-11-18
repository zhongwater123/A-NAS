//go:build linux

// Package montageservice runs the montage center as its own service
// (anas-montage.service, ADR 0038): the Product Service forwards
// /api/v1/montage with the browser's session token, and the service confirms
// the session with the Host Agent itself (storeservice).
package montageservice

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
	"github.com/zhongwater123/A-NAS/internal/montage"
	"github.com/zhongwater123/A-NAS/internal/montageapi"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
	"github.com/zhongwater123/A-NAS/internal/storeservice"
)

type Config struct {
	Root       string
	SocketPath string
	Sessions   storeservice.SessionResolver
	// Grants reaches the photo library's project grants.
	Grants montage.Grants
	// Media, Music and Compute decode, analyse and cut the songs
	// (montage.Options); JobCompute, Editing and Planner are what jobs work
	// with besides them. Without a planner the service makes no film.
	Media      montage.Media
	Music      montage.MusicAnalyzer
	Compute    montage.MusicComputer
	JobCompute montage.Compute
	Editing    montage.Editing
	Planner    montage.Planner
	Logger     *slog.Logger
	// CheckStore accepts Root as the service's store; RequireOwnedStore
	// when nil.
	CheckStore    func(string) error
	RetryInterval time.Duration
}

// ErrStoreUnavailable reports a store that is not the service's own on the
// data volume.
var ErrStoreUnavailable = errors.New("montage store on the data volume is unavailable")

// RequireOwnedStore accepts root only as this service's own private
// directory on the writable, verified data volume, so the montage center's
// records are never written to the system disk.
func RequireOwnedStore(root string) error {
	if err := modules.RequireOwnedStore(root); err != nil {
		return fmt.Errorf("%w: %v", ErrStoreUnavailable, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), ".a-nas-volume.json")); err != nil {
		return fmt.Errorf("%w: %v", ErrStoreUnavailable, err)
	}
	return nil
}

// Proxy forwards the montage API to the service on socketPath; while the
// service cannot be reached, requests answer montage_unavailable.
func Proxy(socketPath string, logger *slog.Logger) http.Handler {
	return serviceproxy.New(socketPath, serviceproxy.Unavailable{
		Code: montageapi.Unavailable, Message: "the montage center is not available",
	}, logger)
}

// Run serves the montage API on SocketPath until ctx ends. While the store
// cannot be opened, as when the data volume is offline or read-only,
// requests answer montage_unavailable. Every store it opens is reconciled
// with the photo library's grants and retries the revocations it missed
// (montage.Store.Upkeep), and analyses the songs waiting in it
// (montage.Store.StartWorkers).
func Run(ctx context.Context, config Config) error {
	if config.Grants == nil {
		return errors.New("the montage service needs the photo library's grants")
	}
	if config.CheckStore == nil {
		config.CheckStore = RequireOwnedStore
	}
	return storeservice.Run(ctx, storeservice.Config[*montage.Store]{
		Name: "montage", Root: config.Root, SocketPath: config.SocketPath, Sessions: config.Sessions,
		Unavailable: montageapi.Unavailable,
		Open: func(ctx context.Context, root string) (*montage.Store, error) {
			store, err := montage.Open(ctx, root, montage.Options{Grants: config.Grants, Media: config.Media, Music: config.Music, Compute: config.Compute, JobCompute: config.JobCompute, Editing: config.Editing, Planner: config.Planner, Logger: config.Logger})
			if err == nil {
				if err = store.StartWorkers(ctx); err != nil {
					_ = store.Close()
				}
			}
			return store, err
		},
		API:            func(store *montage.Store) http.Handler { return montageapi.New(store, config.Logger) },
		Remove:         RemoveAccount,
		Upkeep:         func(ctx context.Context, store *montage.Store) { store.Upkeep(ctx) },
		UpkeepInterval: montage.UpkeepInterval,
		CheckStore:     config.CheckStore, RetryInterval: config.RetryInterval, Logger: config.Logger,
	})
}

// RemoveAccount forgets a deleted account's projects.
func RemoveAccount(store *montage.Store) accountremoval.Remove {
	return func(ctx context.Context, deletion accountid.AccountDeletion) (accountremoval.Result, error) {
		return accountremoval.Result{}, store.RemoveAccount(ctx, deletion.UserID)
	}
}
