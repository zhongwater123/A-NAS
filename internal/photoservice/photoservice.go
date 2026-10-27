// Package photoservice runs the photo service process (ADR 0011). It owns the
// data volume's photos subvolume under its own identity, confirms the session
// behind every request with the Host Agent, and serves the photo API on a
// Unix socket that only the Product Service can reach. Media jobs, trash
// expiry and reconciliation run here without any user session.
package photoservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coreos/go-systemd/v22/activation"
	"golang.org/x/sys/unix"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
)

type SessionResolver interface {
	ResolveSessionUser(context.Context, string) (accounts.SessionUser, error)
}

type Config struct {
	// Root is the photos subvolume, created by the Host Agent.
	Root       string
	SocketPath string
	Sessions   SessionResolver
	// AssistantSocketPath, when set, also serves members' AI assistants, who
	// read through the Policy as their member and change nothing. Accounts
	// confirms whose assistant connected. Its directory is private to this
	// service; systemd binds the socket into each assistant's sandbox.
	AssistantSocketPath string
	Accounts            AccountResolver
	Logger              *slog.Logger
	// CheckStore proves Root is the photo store on the mounted, writable data
	// volume; it defaults to RequireOwnedStore. While it fails, requests
	// answer photos_unavailable and nothing is created.
	CheckStore func(string) error
	// RetryInterval is how often the store is checked, both while it is
	// unavailable and while it is open.
	RetryInterval time.Duration
	Photos        photos.Options
	// AI reaches the AI Worker for background vectors and search; nil leaves
	// local AI off. It replaces Photos.AI.
	AI photos.Embedder
	// AIQuietPeriod is how long the photo library must go unused before AI
	// work starts; it defaults to QuietPeriod.
	AIQuietPeriod time.Duration
	// AIPressure reports a resource's recent stall share in percent; it
	// defaults to the kernel's pressure stall information. Tests replace it
	// so that load on the machine running them cannot hold AI work back.
	AIPressure func(resource string) (float64, error)
	// Vectors is the service's own vector index (ADR 0028), kept in line
	// with the Catalog and compared with searches; nil leaves it off.
	Vectors photos.VectorIndex
}

var ErrStoreUnavailable = errors.New("the photo store is not available")

// RequireOwnedStore accepts root only when it is a directory on the mounted,
// writable Btrfs data volume, owned by this process's user and private to
// it. The Host Agent creates it only on a verified volume, so a missing store
// means the volume is offline and the system disk must not be used instead.
// Btrfs turns read-only after a device error, so a read-only store means the
// volume is failing.
func RequireOwnedStore(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrStoreUnavailable, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || int(stat.Uid) != os.Geteuid() || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("%w: %s must be a private directory of this service", ErrStoreUnavailable, root)
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(root, &fs); err != nil || uint64(fs.Type) != 0x9123683e {
		return fmt.Errorf("%w: %s is not on the Btrfs data volume", ErrStoreUnavailable, root)
	}
	if fs.Flags&unix.ST_RDONLY != 0 {
		return fmt.Errorf("%w: the data volume is read-only", ErrStoreUnavailable)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), ".a-nas-volume.json")); err != nil {
		return fmt.Errorf("%w: the data volume marker is missing", ErrStoreUnavailable)
	}
	return nil
}

// Run serves until ctx ends.
func Run(parent context.Context, config Config) error {
	if config.Sessions == nil || config.Root == "" || config.SocketPath == "" {
		return errors.New("photo service needs a store, a socket and a session lookup")
	}
	if config.AssistantSocketPath != "" && config.Accounts == nil {
		return errors.New("the assistant socket needs an account lookup")
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.CheckStore == nil {
		config.CheckStore = RequireOwnedStore
	}
	// The service usually starts before the Host Agent has prepared the
	// store; checking is a few stat calls, so photos come up within seconds.
	if config.RetryInterval <= 0 {
		config.RetryInterval = 2 * time.Second
	}
	listener, err := listen(config.SocketPath, 0o660)
	if err != nil {
		return err
	}
	var assistantListener net.Listener
	inherited := false
	if config.AssistantSocketPath != "" {
		assistantListener, inherited, err = assistantSocket(config.AssistantSocketPath)
		if err != nil {
			_ = listener.Close()
			return err
		}
	}
	var api atomic.Pointer[http.Handler]
	serve := func(handler http.Handler) { api.Store(&handler) }
	unavailable := photosapi.New(nil, config.Logger)
	serve(unavailable)
	if config.AIQuietPeriod <= 0 {
		config.AIQuietPeriod = QuietPeriod
	}
	gate := NewGate(config.AIQuietPeriod, config.Logger)
	if config.AIPressure != nil {
		gate.pressure = config.AIPressure
	} else if len(gate.watched) > 0 {
		config.Logger.Info("AI work yields to the pressure of", "cgroups", gate.watched)
	} else {
		config.Logger.Info("AI work yields to the whole system's pressure")
	}
	current := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		(*api.Load()).ServeHTTP(w, r)
	})
	server := &http.Server{
		Handler:           Authenticate(config.Sessions, config.Logger, gate, current),
		ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20,
	}
	serveError := make(chan error, 2)
	go func() { serveError <- server.Serve(listener) }()
	var assistantServer *http.Server
	if assistantListener != nil {
		assistantServer = &http.Server{
			Handler:           authenticateAssistant(config.Accounts, config.Logger, gate, assistantAPI(current)),
			ConnContext:       withPeerUID,
			ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20,
		}
		go func() { serveError <- assistantServer.Serve(assistantListener) }()
	}

	// As in the Product Service, background work stops as soon as the
	// process is told to stop, while open requests drain; the Catalog closes
	// only after both, so an upload in progress still lands (#192).
	serving, stop := context.WithCancel(context.WithoutCancel(parent))
	supervised := make(chan struct{})
	go func() {
		defer close(supervised)
		supervise(serving, parent, config, gate, serve, unavailable)
	}()
	defer func() {
		stop()
		<-supervised
	}()

	select {
	case err := <-serveError:
		return err
	case <-parent.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	var shutdownErr error
	if assistantServer != nil {
		shutdownErr = assistantServer.Shutdown(shutdown)
		if !inherited {
			_ = os.Remove(config.AssistantSocketPath)
		}
	}
	shutdownErr = errors.Join(shutdownErr, server.Shutdown(shutdown))
	_ = os.Remove(config.SocketPath)
	return shutdownErr
}

// requirePrivateDirectory accepts dir only when it is a directory of this
// service that nobody else may enter.
func requirePrivateDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || int(stat.Uid) != os.Geteuid() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s must be a directory private to the photo service: its assistant socket is open to every UID", dir)
	}
	return nil
}

// shutdownGrace is how long stopping waits for open requests; the unit's
// TimeoutStopSec leaves room for closing the Catalog after it.
const shutdownGrace = 15 * time.Second

// supervise keeps the photo API backed by an open store until ctx ends, and
// runs background work until background ends. It
// opens the store once it passes its check and closes it again as soon as it
// stops passing or its path names another directory, as when the data volume
// goes offline, turns read-only or is remounted. The open Catalog never
// follows the path onto the system disk, and reopening reconciles whatever an
// abrupt loss left behind. Requests in between answer photos_unavailable.
func supervise(ctx, background context.Context, config Config, gate *Gate, serve func(http.Handler), unavailable http.Handler) {
	for {
		service, err := openWhenReady(ctx, config)
		if err != nil {
			return
		}
		workers, stopWorkers := context.WithCancel(background)
		background := make(chan struct{})
		go func() {
			defer close(background)
			RunBackground(workers, service, config.Logger, config.AI, gate, config.Vectors)
		}()
		serve(photosapi.New(service, config.Logger))
		config.Logger.Info("photo service ready", "store", config.Root, "socket", config.SocketPath)

		lost := watchStore(ctx, config, service)
		serve(unavailable)
		stopWorkers()
		<-background
		_ = service.Close()
		if lost == nil {
			return
		}
		config.Logger.Error("photo store lost; photos are unavailable until it returns", "error", lost)
	}
}

// watchStore returns nil when ctx ends, or why the open store can no longer
// be used.
func watchStore(ctx context.Context, config Config, service *photos.Service) error {
	ticker := time.NewTicker(config.RetryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		if err := config.CheckStore(config.Root); err != nil {
			return err
		}
		opened, err := service.StoreInfo()
		if err != nil {
			return fmt.Errorf("%w: %v", ErrStoreUnavailable, err)
		}
		current, err := os.Lstat(config.Root)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrStoreUnavailable, err)
		}
		if !os.SameFile(opened, current) {
			return fmt.Errorf("%w: %s now names another directory", ErrStoreUnavailable, config.Root)
		}
	}
}

// openWhenReady waits until the store passes its check, then opens it. It
// returns an error only when ctx ends first.
func openWhenReady(ctx context.Context, config Config) (*photos.Service, error) {
	logged := false
	for {
		err := config.CheckStore(config.Root)
		if err == nil {
			options := config.Photos
			options.AI = config.AI
			service, openErr := photos.Open(config.Root, options)
			if openErr == nil {
				return service, nil
			}
			err = openErr
		}
		if !logged {
			config.Logger.Warn("photo store unavailable; waiting", "error", err)
			logged = true
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(config.RetryInterval):
		}
	}
}

// RunBackground readies the job queue (photos.Service.RecoverJobs), then
// renders media, runs local AI through ai while gate allows it, keeps the
// vector index in line and compares searches with it, and maintains the
// store until ctx ends. It returns only once all of them have stopped, so
// the caller may then close service. None needs a user session; a nil ai
// leaves local AI off, and nil vectors the vector index.
func RunBackground(ctx context.Context, service *photos.Service, logger *slog.Logger, ai photos.Embedder, gate *Gate,
	vectors photos.VectorIndex) {
	if recovery, err := service.RecoverJobs(ctx); err != nil {
		logger.ErrorContext(ctx, "photo job recovery failed", "error", err)
	} else if recovery != (photos.JobRecovery{}) {
		logger.InfoContext(ctx, "photo jobs recovered", "recovery", recovery)
	}
	var workers sync.WaitGroup
	workers.Go(func() {
		service.RunMedia(ctx, time.Minute, func(err error) {
			logger.ErrorContext(ctx, "photo media job failed", "error", err)
		})
	})
	if ai != nil {
		workers.Go(func() {
			// Check again no later than the quiet period could have passed.
			service.RunAI(ctx, ai, gate, min(time.Minute, gate.quiet), func(err error) {
				logger.ErrorContext(ctx, "photo AI job failed", "error", err)
			})
		})
	}
	if vectors != nil {
		runVectorIndex(ctx, &workers, service, logger, gate, vectors)
	} else if err := service.ForgetVectorIndex(ctx); err != nil {
		logger.ErrorContext(ctx, "photo vector index state could not be dropped", "error", err)
	}
	workers.Go(func() { maintain(ctx, service, logger) })
	workers.Wait()
}

// runVectorIndex starts keeping the vector index in line with the Catalog,
// as background work, and comparing searches with it
// (docs/architecture/vector-index.md). The comparisons are what decides
// whether search moves to the index; they log no query or name.
func runVectorIndex(ctx context.Context, workers *sync.WaitGroup, service *photos.Service, logger *slog.Logger, gate *Gate,
	vectors photos.VectorIndex) {
	workers.Go(func() {
		service.RunVectorSync(ctx, vectors, gate, min(time.Minute, gate.quiet), func(event photos.VectorSyncEvent) {
			level := slog.LevelInfo
			if event.Rejected > 0 {
				// The index refused these vectors; reconciliation tries them
				// again tomorrow.
				level = slog.LevelWarn
			}
			logger.Log(ctx, level, "photo vector index "+event.Kind, "collection", event.Collection,
				"upserted", event.Upserted, "deleted", event.Deleted, "rejected", event.Rejected, "queued", event.Queued)
		}, func(err error) {
			logger.WarnContext(ctx, "photo vector index sync failed; it retries", "error", err)
		})
	})
	var warm func() bool
	if client, ok := vectors.(interface{ Warm() bool }); ok {
		warm = client.Warm
	}
	workers.Go(func() {
		service.RunVectorShadow(ctx, vectors, warm, min(time.Minute, gate.quiet), func(c photos.ShadowComparison) {
			logger.InfoContext(ctx, "photo vector index shadow search", "collection", c.Collection, "candidates", c.Candidates,
				"overlap10", c.Overlap[10], "overlap100", c.Overlap[100], "overlap500", c.Overlap[500],
				"index_ms", c.Index.Milliseconds(), "exact_ms", c.Exact.Milliseconds(), "warm", c.Warm)
		}, func(err error) {
			logger.WarnContext(ctx, "photo vector index shadow search failed", "error", err)
		})
	})
}

// maintain repairs the store after an unclean stop and then expires trashed
// photos hourly.
func maintain(ctx context.Context, service *photos.Service, logger *slog.Logger) {
	if report, err := service.Reconcile(ctx); err != nil {
		logger.ErrorContext(ctx, "photo reconciliation failed", "error", err)
	} else if report != (photos.ReconcileReport{}) {
		logger.InfoContext(ctx, "photo store reconciled", "report", report)
	}
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if _, err := service.ExpireTrash(ctx); err != nil && ctx.Err() == nil {
			logger.ErrorContext(ctx, "photo trash expiry failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Proxy forwards the photo API to the photo service on socketPath; while
// the service cannot be reached, requests answer photos_unavailable.
func Proxy(socketPath string, logger *slog.Logger) http.Handler {
	return serviceproxy.New(socketPath, serviceproxy.Unavailable{
		Code: "photos_unavailable", Message: "the photo library is not available on this device",
	}, logger)
}

// Local serves the photo API of service in this process, as development
// runs it in the Product Service: with the photo service's authentication
// and gate, behind the request rewriting of Proxy.
func Local(service *photos.Service, sessions SessionResolver, gate *Gate, logger *slog.Logger) http.Handler {
	return serviceproxy.Local(Authenticate(sessions, logger, gate, photosapi.New(service, logger)))
}

// Authenticate turns the forwarded session token into the caller's
// Principal (serviceproxy.SessionHeader). The photo service trusts only the
// Host Agent's answer, never the Product Service; development, which runs
// the photo API in the Product Service, asks its own account store the same
// way. Each request by a signed-in person keeps background AI work waiting
// until it is served, such as a video playing or a long upload, except
// reading the AI status, which a window may poll while left open.
func Authenticate(sessions SessionResolver, logger *slog.Logger, gate *Gate, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := sessions.ResolveSessionUser(r.Context(), r.Header.Get(serviceproxy.SessionHeader))
		switch {
		case errors.Is(err, accounts.ErrSessionNotFound):
			httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
			return
		case err != nil:
			logger.ErrorContext(r.Context(), "session lookup failed", "error", err)
			httpjson.WriteError(w, http.StatusServiceUnavailable, "photos_unavailable", "the photo library is not available on this device")
			return
		case user.MustChangePassword:
			httpjson.WriteError(w, http.StatusForbidden, "password_change_required", "choose a new password before continuing")
			return
		}
		if r.URL.Path != photosapi.AIStatusPath {
			done := gate.Use()
			defer done()
		}
		principal := photosapi.NewPrincipal(user.UserID, user.Username, user.Role == accounts.RoleAdmin, user.Viewing)
		next.ServeHTTP(w, r.WithContext(photosapi.WithPrincipal(r.Context(), principal)))
	})
}

// listen opens an API socket. The Product Service's socket is 0660, for its
// user is a member of this service's group; the runtime directory keeps
// everyone else out.
// assistantSocket is where members' assistants connect; the peer's UID says
// which member. When systemd holds the socket (a socket unit naming it
// "assistant"), it outlives restarts of this service, so the socket bound into
// running sandboxes keeps working; the unit keeps its directory private.
// Otherwise the service makes it, open to every UID, in a directory that must
// keep out all but the sandboxes systemd binds the socket into.
func assistantSocket(socketPath string) (net.Listener, bool, error) {
	if listeners, err := activation.ListenersWithNames(); err == nil && len(listeners["assistant"]) > 0 {
		return listeners["assistant"][0], true, nil
	}
	if err := requirePrivateDirectory(filepath.Dir(socketPath)); err != nil {
		return nil, false, err
	}
	listener, err := listen(socketPath, 0o666)
	return listener, false, err
}

func listen(socketPath string, mode os.FileMode) (net.Listener, error) {
	if info, err := os.Lstat(socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", socketPath)
		}
		if err := os.Remove(socketPath); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socketPath, mode); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}
