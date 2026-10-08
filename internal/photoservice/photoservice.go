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

	"golang.org/x/sys/unix"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
)

type SessionResolver interface {
	ResolveSessionUser(context.Context, string) (accounts.SessionUser, error)
}

type Config struct {
	// Root is the photos subvolume, created by the Host Agent.
	Root       string
	SocketPath string
	Sessions   SessionResolver
	Logger     *slog.Logger
	// CheckStore proves Root is the photo store on the mounted, writable data
	// volume; it defaults to RequireOwnedStore. While it fails, requests
	// answer photos_unavailable and nothing is created.
	CheckStore func(string) error
	// RetryInterval is how often the store is checked, both while it is
	// unavailable and while it is open.
	RetryInterval time.Duration
	Photos        photos.Options
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
	listener, err := listen(config.SocketPath)
	if err != nil {
		return err
	}
	var api atomic.Pointer[http.Handler]
	serve := func(handler http.Handler) { api.Store(&handler) }
	unavailable := photosapi.New(nil, config.Logger)
	serve(unavailable)
	server := &http.Server{
		Handler: authenticate(config.Sessions, config.Logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			(*api.Load()).ServeHTTP(w, r)
		})),
		ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20,
	}
	serveError := make(chan error, 1)
	go func() { serveError <- server.Serve(listener) }()

	ctx, stop := context.WithCancel(parent)
	supervised := make(chan struct{})
	go func() {
		defer close(supervised)
		supervise(ctx, config, serve, unavailable)
	}()
	// The Catalog closes only after requests and workers using it have
	// stopped.
	defer func() {
		stop()
		<-supervised
	}()

	select {
	case err := <-serveError:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		return err
	}
	_ = os.Remove(config.SocketPath)
	return nil
}

// supervise keeps the photo API backed by an open store until ctx ends. It
// opens the store once it passes its check and closes it again as soon as it
// stops passing or its path names another directory, as when the data volume
// goes offline, turns read-only or is remounted. The open Catalog never
// follows the path onto the system disk, and reopening reconciles whatever an
// abrupt loss left behind. Requests in between answer photos_unavailable.
func supervise(ctx context.Context, config Config, serve func(http.Handler), unavailable http.Handler) {
	for {
		service, err := openWhenReady(ctx, config)
		if err != nil {
			return
		}
		workers, stopWorkers := context.WithCancel(ctx)
		background := make(chan struct{})
		go func() {
			defer close(background)
			RunBackground(workers, service, config.Logger)
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
			service, openErr := photos.Open(config.Root, config.Photos)
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

// RunBackground renders media and maintains the store until ctx ends. It
// returns only once both have stopped, so the caller may then close service.
// Neither needs a user session.
func RunBackground(ctx context.Context, service *photos.Service, logger *slog.Logger) {
	var workers sync.WaitGroup
	workers.Go(func() {
		service.RunMedia(ctx, time.Minute, func(err error) {
			logger.ErrorContext(ctx, "photo media job failed", "error", err)
		})
	})
	workers.Go(func() { maintain(ctx, service, logger) })
	workers.Wait()
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

// authenticate turns the forwarded session token into the caller's
// Principal. It trusts the Host Agent's answer, never the Product Service.
func authenticate(sessions SessionResolver, logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := sessions.ResolveSessionUser(r.Context(), r.Header.Get(photosapi.SessionHeader))
		switch {
		case errors.Is(err, accounts.ErrSessionNotFound):
			photosapi.WriteError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
			return
		case err != nil:
			logger.ErrorContext(r.Context(), "session lookup failed", "error", err)
			photosapi.WriteError(w, http.StatusServiceUnavailable, "photos_unavailable", "the photo library is not available on this device")
			return
		case user.MustChangePassword:
			photosapi.WriteError(w, http.StatusForbidden, "password_change_required", "choose a new password before continuing")
			return
		}
		principal := photosapi.NewPrincipal(user.UserID, user.Username, user.Role == accounts.RoleAdmin, user.Viewing)
		next.ServeHTTP(w, r.WithContext(photosapi.WithPrincipal(r.Context(), principal)))
	})
}

// listen opens the API socket for the Product Service, whose user is a member
// of this service's group; the runtime directory keeps everyone else out.
func listen(socketPath string) (net.Listener, error) {
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
	if err := os.Chmod(socketPath, 0o660); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}
