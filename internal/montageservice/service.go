//go:build linux

// Package montageservice runs the montage center as its own service
// (anas-montage.service, ADR 0038): the Product Service forwards
// /api/v1/montage with the browser's session token, and the service confirms
// the session with the Host Agent itself.
package montageservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/accountremoval"
	"github.com/zhongwater123/A-NAS/internal/modules"
	"github.com/zhongwater123/A-NAS/internal/montage"
	"github.com/zhongwater123/A-NAS/internal/montageapi"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
)

// SessionResolver confirms forwarded sessions and the deletions of
// accounts; on a NAS it is the Host Agent's session lookup.
type SessionResolver interface {
	modules.SessionResolver
	AccountDeletion(context.Context, string) (accountid.AccountDeletion, error)
}

type Config struct {
	Root       string
	SocketPath string
	Sessions   SessionResolver
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

// shutdownGrace is how long stopping waits for open requests.
const shutdownGrace = 15 * time.Second

// Ready is logged once the store is open; the site check waits for it.
const Ready = "montage service ready"

// upkeepInterval is how often reconciling and revoking are tried.
const upkeepInterval = time.Minute

// Run serves the montage API on SocketPath until ctx ends. While the store
// cannot be opened, as when the data volume is offline or read-only,
// requests answer montage_unavailable.
func Run(parent context.Context, config Config) error {
	if config.Root == "" || config.SocketPath == "" || config.Sessions == nil || config.Grants == nil {
		return errors.New("montage service needs a store, an API socket, sessions and the photo library's grants")
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.CheckStore == nil {
		config.CheckStore = RequireOwnedStore
	}
	if config.RetryInterval <= 0 {
		config.RetryInterval = 2 * time.Second
	}
	listener, err := modules.Listener(config.SocketPath)
	if err != nil {
		return err
	}
	// The Product Service reaches the socket through this service's group;
	// the runtime directory keeps everyone else out.
	if err := os.Chmod(config.SocketPath, 0o660); err != nil {
		_ = listener.Close()
		return err
	}
	defer func() { _ = os.Remove(config.SocketPath); _ = listener.Close() }()
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	defer cancel()
	var current atomic.Pointer[http.Handler]
	unavailable := montageapi.New(nil, config.Logger)
	current.Store(&unavailable)
	served := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		(*current.Load()).ServeHTTP(w, r)
	})
	// A deleted account leaves on the Product Service's request, which
	// carries no session; what to do comes from the Host Agent.
	routes := http.NewServeMux()
	routes.Handle(accountremoval.Path, served)
	routes.Handle("/", modules.Authenticate(config.Sessions, montageapi.Unavailable, config.Logger, served))
	server := &http.Server{
		Handler:           routes,
		ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 1 << 20,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	supervised := make(chan struct{})
	go func() {
		defer close(supervised)
		supervise(ctx, config, func(h http.Handler) { current.Store(&h) }, unavailable)
	}()
	defer func() {
		cancel()
		<-supervised
	}()
	select {
	case <-parent.Done():
	case err = <-serveErr:
		return err
	}
	shutdown, stop := context.WithTimeout(context.Background(), shutdownGrace)
	defer stop()
	if err := server.Shutdown(shutdown); err != nil {
		_ = server.Close()
		return err
	}
	return nil
}

// RemoveAccount forgets a deleted account's projects.
func RemoveAccount(store *montage.Store) accountremoval.Remove {
	return func(ctx context.Context, deletion accountid.AccountDeletion) (accountremoval.Result, error) {
		return accountremoval.Result{}, store.RemoveAccount(ctx, deletion.UserID)
	}
}

// supervise opens the store when it is ready, serves it, and takes it out of
// service when the data volume goes away or turns read-only.
func supervise(ctx context.Context, config Config, serve func(http.Handler), unavailable http.Handler) {
	ticker := time.NewTicker(config.RetryInterval)
	defer ticker.Stop()
	warned := false
	for ctx.Err() == nil {
		store, opened, err := open(ctx, config)
		if err != nil {
			if !warned {
				config.Logger.WarnContext(ctx, "montage store unavailable; waiting", "error", err)
				warned = true
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				continue
			}
		}
		if err := store.ReclaimSongs(ctx); err != nil {
			config.Logger.WarnContext(ctx, "reclaiming song copies failed", "error", err)
		}
		analysis, stopAnalysis := context.WithCancel(ctx)
		analysed := make(chan struct{})
		go func() { defer close(analysed); store.AnalyzeSongs(analysis) }()
		jobsDone := runJobs(ctx, store, config.Logger)
		closeStore := func() {
			jobsDone()
			stopAnalysis()
			<-analysed
			_ = store.Close()
		}
		serve(accountremoval.Route(config.Sessions.AccountDeletion, RemoveAccount(store), config.Logger,
			montageapi.New(store, config.Logger)))
		config.Logger.Info(Ready)
		warned = false
		// The projects are reconciled with the photo library's grants once
		// it answers; revocations it missed are tried again every minute.
		reconciled, nextUpkeep := false, time.Time{}
		for {
			select {
			case <-ctx.Done():
				serve(unavailable)
				closeStore()
				return
			case <-ticker.C:
			}
			if now := time.Now(); now.After(nextUpkeep) {
				nextUpkeep = now.Add(upkeepInterval)
				if !reconciled {
					if err := store.Reconcile(ctx); err != nil {
						config.Logger.WarnContext(ctx, "reconciling projects with the photo library failed; trying again", "error", err)
					} else {
						reconciled = true
					}
				}
				if err := store.RetryRevocations(ctx); err != nil {
					config.Logger.WarnContext(ctx, "retrying grant revocations failed", "error", err)
				}
			}
			current, err := os.Lstat(config.Root)
			if err = errors.Join(err, config.CheckStore(config.Root)); err != nil || !os.SameFile(opened, current) {
				serve(unavailable)
				closeStore()
				config.Logger.WarnContext(ctx, "montage store unavailable", "error", err)
				break
			}
		}
	}
}

// runJobs puts the jobs a stop cut short back in the queue and runs the
// jobs until the returned function stops them, which returns once the
// running step has stopped: the job goes on from it at the next start.
func runJobs(ctx context.Context, store *montage.Store, logger *slog.Logger) func() {
	jobsCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := store.RecoverJobs(jobsCtx); err != nil {
			logger.ErrorContext(jobsCtx, "recovering montage jobs failed; no job runs", "error", err)
			return
		}
		store.RunJobs(jobsCtx)
	}()
	return func() {
		cancel()
		<-done
	}
}

func open(ctx context.Context, config Config) (*montage.Store, os.FileInfo, error) {
	if err := config.CheckStore(config.Root); err != nil {
		return nil, nil, err
	}
	opened, err := os.Lstat(config.Root)
	if err != nil {
		return nil, nil, err
	}
	store, err := montage.Open(ctx, config.Root, montage.Options{Grants: config.Grants, Media: config.Media, Music: config.Music,
		Compute: config.Compute, JobCompute: config.JobCompute, Editing: config.Editing, Planner: config.Planner, Logger: config.Logger})
	if err != nil {
		return nil, nil, err
	}
	return store, opened, nil
}
