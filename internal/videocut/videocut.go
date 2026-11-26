//go:build linux

// Package videocut runs VideoCut as its own service
// (anas-videocut.service, ADR 0038): the Product Service forwards
// /api/v1/videocut with the browser's session token, and the service confirms
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
	"github.com/zhongwater123/A-NAS/internal/videocut/external"
	"github.com/zhongwater123/A-NAS/internal/videocut/protocol"
	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

// The Workers' clients provide what app asks of them.
var (
	_ app.Compute       = videocutcompute.Client{}
	_ app.MusicComputer = videocutcompute.Client{}
	_ app.MusicAnalyzer = external.MusicWorker{}
)

// Grants and Media are what the Product Service hands VideoCut: the photo
// library's project grants, and the FFmpeg Worker that decodes, probes and
// renders (app.Editing too when it is one).
type (
	Grants = app.Grants
	Media  = app.Media
)

// Workers are what VideoCut works with besides the photo library, the same
// in the service and in development. MusicSocket reaches the music
// analysis Worker and ComputeSocket the compute Worker: while either is
// empty, songs wait for it. PlanAnswers names a directory of fixed plans
// that stand in for planning (external.FixedPlanner); without it the
// store has no planner of its own.
type Workers struct {
	Media                      Media
	MusicSocket, ComputeSocket string
	PlanAnswers                string
}

// options are the store's options with w: Run and Local open the store
// with them.
func (w Workers) options(grants Grants, markers app.Markers, logger *slog.Logger) app.Options {
	editing, _ := w.Media.(app.Editing)
	options := app.Options{Grants: grants, Media: w.Media, Editing: editing, WindowSearch: WindowSearchOf(grants),
		Markers: markers, Logger: logger}
	if w.MusicSocket != "" {
		options.Music = external.MusicWorker{Socket: w.MusicSocket}
	}
	if w.ComputeSocket != "" {
		client := videocutcompute.Client{SocketPath: w.ComputeSocket}
		options.Compute, options.JobCompute = client, client
	}
	if w.PlanAnswers != "" {
		options.Planner = external.FixedPlanner{Dir: w.PlanAnswers}
	}
	return options
}

// WindowSearchOf is the grants' own search of windows by text, for editing
// a film (#596), when they have one: the photo library's answers as the
// VideoCut's errors (external.GrantSearch).
func WindowSearchOf(grants app.Grants) app.WindowSearch {
	if search, ok := grants.(external.PhotoSearch); ok {
		return external.GrantSearch{Grants: search}
	}
	return nil
}

type Config struct {
	Root       string
	SocketPath string
	Sessions   storeservice.SessionResolver
	// Grants reaches the photo library's project grants.
	Grants Grants
	// Workers decode, analyse and cut the songs and work the jobs.
	// Without a planner the service makes no film.
	Workers
	// Accounts names the account behind a member's UID; AssistantSocket is
	// where members' planning instances and root's planner reach VideoCut
	// when systemd does not hold it; PlanMarkers is the directory of
	// planning markers the planner reads
	// (docs/architecture/videocut-planning-session.md).
	Accounts        protocol.AccountResolver
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
var ErrStoreUnavailable = errors.New("VideoCut store on the data volume is unavailable")

// RequireOwnedStore accepts root only as this service's own private
// directory on the writable, verified data volume, so VideoCut's
// records are never written to the system disk.
func RequireOwnedStore(root string) error {
	if err := modules.RequireDataVolumeStore(root); err != nil {
		return fmt.Errorf("%w: %v", ErrStoreUnavailable, err)
	}
	return nil
}

// Proxy forwards the VideoCut API to the service on socketPath; while the
// service cannot be reached, requests answer videocut_unavailable.
func Proxy(socketPath string, logger *slog.Logger) http.Handler {
	return serviceproxy.New(socketPath, serviceproxy.Unavailable{
		Code: protocol.Unavailable, Message: "VideoCut is not available",
	}, logger)
}

// Run serves the VideoCut API on SocketPath until ctx ends. While the store
// cannot be opened, as when the data volume is offline or read-only,
// requests answer videocut_unavailable. Every store it opens is reconciled
// with the photo library's grants and retries the revocations it missed
// (app.Store.Upkeep), and analyses the songs waiting in it
// (app.Store.StartWorkers).
func Run(ctx context.Context, config Config) error {
	if config.Grants == nil {
		return errors.New("the VideoCut service needs the photo library's grants")
	}
	if config.CheckStore == nil {
		config.CheckStore = RequireOwnedStore
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	// The planning markers live in the service's runtime directory, where
	// root's planner reads them; they come back from the jobs at start.
	var markers app.Markers
	if config.PlanMarkers != "" {
		if err := os.MkdirAll(config.PlanMarkers, 0o750); err != nil {
			return err
		}
		markers = external.PlanMarkers{Dir: config.PlanMarkers}
	}
	// Members' planning instances reach VideoCut on its assistant
	// socket, where their UID says who they are, and so does root's planner,
	// naming the member. It answers with the store that is open, and
	// unavailable while none is.
	assistant := &protocol.AssistantGate{}
	stopAssistant, err := protocol.ListenAssistant(ctx, config.Accounts, config.AssistantSocket, config.Logger, assistant)
	if err != nil {
		return err
	}
	defer stopAssistant()
	return storeservice.Run(ctx, storeservice.Config[*servedStore]{
		Name: "videocut", Root: config.Root, SocketPath: config.SocketPath, Sessions: config.Sessions,
		Unavailable: protocol.Unavailable,
		Open: func(ctx context.Context, root string) (*servedStore, error) {
			store, err := app.Open(ctx, root, config.options(config.Grants, markers, config.Logger))
			if err != nil {
				return nil, err
			}
			if err := store.StartWorkers(ctx); err != nil {
				return nil, errors.Join(err, store.Close())
			}
			assistant.Serve(protocol.Assistant(store, config.Logger))
			return &servedStore{Store: store, assistant: assistant}, nil
		},
		API:            func(store *servedStore) http.Handler { return protocol.New(store.Store, config.Logger) },
		Remove:         func(store *servedStore) accountremoval.Remove { return RemoveAccount(store.Store) },
		Upkeep:         func(ctx context.Context, store *servedStore) { store.Upkeep(ctx) },
		UpkeepInterval: app.UpkeepInterval,
		CheckStore:     config.CheckStore, RetryInterval: config.RetryInterval, Logger: config.Logger,
	})
}

// LocalConfig is VideoCut in development (Local): the photo library's
// grants in the Product Service's process, the Workers, and Serve, which
// mounts the API.
type LocalConfig struct {
	Grants Grants
	Workers
	Serve  bool
	Logger *slog.Logger
}

// Local runs VideoCut in the Product Service's process, as development
// does, with its store in root, kept as the service keeps it
// (app.Store.Upkeep). It returns the API, nil unless config.Serve, which
// also starts the jobs and the song analysis (app.Store.StartWorkers); a
// removal of deleted accounts, which runs either way; and a function that
// stops the store. The API's requests carry the headers the service's
// socket would. Development leaves no planning markers.
func Local(ctx context.Context, root string, config LocalConfig) (http.Handler, func(context.Context, string) error, func(), error) {
	store, err := app.Open(ctx, root, config.options(config.Grants, nil, config.Logger))
	if err != nil {
		return nil, nil, nil, err
	}
	stopUpkeep := storeservice.Every(ctx, app.UpkeepInterval, store.Upkeep)
	closeStore := func() {
		stopUpkeep()
		_ = store.Close()
	}
	if !config.Serve {
		return nil, store.RemoveAccount, closeStore, nil
	}
	if err := store.StartWorkers(ctx); err != nil {
		closeStore()
		return nil, nil, nil, err
	}
	return serviceproxy.Local(protocol.New(store, config.Logger)), store.RemoveAccount, closeStore, nil
}

// RemoveAccount forgets a deleted account's projects.
func RemoveAccount(store *app.Store) accountremoval.Remove {
	return func(ctx context.Context, deletion accountid.AccountDeletion) (accountremoval.Result, error) {
		return accountremoval.Result{}, store.RemoveAccount(ctx, deletion.UserID)
	}
}
