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
	"sync/atomic"
	"syscall"
	"time"

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
	// CheckStore proves Root is the photo store on the mounted data volume;
	// it defaults to RequireOwnedStore. Until it passes, requests answer
	// photos_unavailable and nothing is created.
	CheckStore func(string) error
	// RetryInterval is how often an unavailable store is checked again.
	RetryInterval time.Duration
	Photos        photos.Options
}

var ErrStoreUnavailable = errors.New("the photo store is not available")

// RequireOwnedStore accepts root only when it is a directory on the mounted
// Btrfs data volume, owned by this process's user and private to it. The
// Host Agent creates it only on a verified volume, so a missing store means
// the volume is offline and the system disk must not be used instead.
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
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), ".a-nas-volume.json")); err != nil {
		return fmt.Errorf("%w: the data volume marker is missing", ErrStoreUnavailable)
	}
	return nil
}

// Run serves until ctx ends.
func Run(ctx context.Context, config Config) error {
	if config.Sessions == nil || config.Root == "" || config.SocketPath == "" {
		return errors.New("photo service needs a store, a socket and a session lookup")
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.CheckStore == nil {
		config.CheckStore = RequireOwnedStore
	}
	if config.RetryInterval <= 0 {
		config.RetryInterval = 30 * time.Second
	}
	listener, err := listen(config.SocketPath)
	if err != nil {
		return err
	}
	var api atomic.Pointer[http.Handler]
	unavailable := photosapi.New(nil, config.Logger)
	api.Store(&unavailable)
	server := &http.Server{
		Handler: authenticate(config.Sessions, config.Logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			(*api.Load()).ServeHTTP(w, r)
		})),
		ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20,
	}
	serveError := make(chan error, 1)
	go func() { serveError <- server.Serve(listener) }()

	service, err := openWhenReady(ctx, config)
	if err == nil {
		defer service.Close()
		ready := photosapi.New(service, config.Logger)
		api.Store(&ready)
		go service.RunMedia(ctx, time.Minute, func(err error) {
			config.Logger.ErrorContext(ctx, "photo media job failed", "error", err)
		})
		go maintain(ctx, service, config.Logger)
		config.Logger.Info("photo service ready", "store", config.Root, "socket", config.SocketPath)
	}

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
