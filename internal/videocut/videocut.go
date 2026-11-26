//go:build linux

// Package videocut runs the montage center as its own service
// (anas-montage.service, ADR 0038): the Product Service forwards
// /api/v1/montage with the browser's session token, and the service confirms
// the session with the Host Agent itself (storeservice).
package videocut

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/accountremoval"
	"github.com/zhongwater123/A-NAS/internal/modules"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
	"github.com/zhongwater123/A-NAS/internal/storeservice"
	"github.com/zhongwater123/A-NAS/internal/videocut/app"
	"github.com/zhongwater123/A-NAS/internal/videocut/protocol"
)

type Config struct {
	Root       string
	SocketPath string
	Sessions   storeservice.SessionResolver
	// Grants reaches the photo library's project grants.
	Grants app.Grants
	// Media, Music and Compute decode, analyse and cut the songs
	// (app.Options); JobCompute, Editing and Planner are what jobs work
	// with besides them. Without a planner the service makes no film.
	Media      app.Media
	Music      app.MusicAnalyzer
	Compute    app.MusicComputer
	JobCompute app.Compute
	Editing    app.Editing
	Planner    app.Planner
	// Accounts names the account behind a member's UID; AssistantSocket is
	// where members' planning instances and root's planner reach the montage
	// center when systemd does not hold it; PlanMarkers is the directory of
	// planning markers the planner reads
	// (docs/architecture/montage-planning-session.md).
	Accounts        AccountResolver
	AssistantSocket string
	PlanMarkers     string
	Logger          *slog.Logger
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
	if err := modules.RequireDataVolumeStore(root); err != nil {
		return fmt.Errorf("%w: %v", ErrStoreUnavailable, err)
	}
	return nil
}

// Proxy forwards the montage API to the service on socketPath; while the
// service cannot be reached, requests answer montage_unavailable.
func Proxy(socketPath string, logger *slog.Logger) http.Handler {
	return serviceproxy.New(socketPath, serviceproxy.Unavailable{
		Code: protocol.Unavailable, Message: "the montage center is not available",
	}, logger)
}

// Run serves the montage API on SocketPath until ctx ends. While the store
// cannot be opened, as when the data volume is offline or read-only,
// requests answer montage_unavailable. Every store it opens is reconciled
// with the photo library's grants and retries the revocations it missed
// (app.Store.Upkeep), and analyses the songs waiting in it
// (app.Store.StartWorkers).
func Run(ctx context.Context, config Config) error {
	if config.Grants == nil {
		return errors.New("the montage service needs the photo library's grants")
	}
	if config.CheckStore == nil {
		config.CheckStore = RequireOwnedStore
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	// The planning markers live in the service's runtime directory, where
	// root's planner reads them; they come back from the jobs at start.
	if config.PlanMarkers != "" {
		if err := os.MkdirAll(config.PlanMarkers, 0o750); err != nil {
			return err
		}
	}
	// Members' planning instances reach the montage center on its assistant
	// socket, where their UID says who they are, and so does root's planner,
	// naming the member. It answers with the store that is open, and
	// unavailable while none is.
	assistant := &assistantGate{}
	stopAssistant, err := listenAssistant(ctx, config, assistant)
	if err != nil {
		return err
	}
	defer stopAssistant()
	return storeservice.Run(ctx, storeservice.Config[*servedStore]{
		Name: "montage", Root: config.Root, SocketPath: config.SocketPath, Sessions: config.Sessions,
		Unavailable: protocol.Unavailable,
		Open: func(ctx context.Context, root string) (*servedStore, error) {
			store, err := app.Open(ctx, root, app.Options{Grants: config.Grants, Media: config.Media, Music: config.Music, Compute: config.Compute,
				JobCompute: config.JobCompute, Editing: config.Editing, Planner: config.Planner, PlanMarkers: config.PlanMarkers, Logger: config.Logger})
			if err != nil {
				return nil, err
			}
			if err := store.StartWorkers(ctx); err != nil {
				return nil, errors.Join(err, store.Close())
			}
			assistant.serve(protocol.Assistant(store, config.Logger))
			return &servedStore{Store: store, assistant: assistant}, nil
		},
		API:            func(store *servedStore) http.Handler { return protocol.New(store.Store, config.Logger) },
		Remove:         func(store *servedStore) accountremoval.Remove { return RemoveAccount(store.Store) },
		Upkeep:         func(ctx context.Context, store *servedStore) { store.Upkeep(ctx) },
		UpkeepInterval: app.UpkeepInterval,
		CheckStore:     config.CheckStore, RetryInterval: config.RetryInterval, Logger: config.Logger,
	})
}

// RemoveAccount forgets a deleted account's projects.
func RemoveAccount(store *app.Store) accountremoval.Remove {
	return func(ctx context.Context, deletion accountid.AccountDeletion) (accountremoval.Result, error) {
		return accountremoval.Result{}, store.RemoveAccount(ctx, deletion.UserID)
	}
}
