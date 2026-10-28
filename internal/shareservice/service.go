//go:build linux

// Package shareservice runs the isolated member-sharing service. It never opens
// member paths: the session-authorized bridge supplies source descriptors and
// stages target files under the recipient's identity.
package shareservice

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

	"golang.org/x/sys/unix"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/mediaworker"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
	"github.com/zhongwater123/A-NAS/internal/sharefiles"
	"github.com/zhongwater123/A-NAS/internal/shares"
	"github.com/zhongwater123/A-NAS/internal/sharesapi"
)

type SessionResolver interface {
	ResolveSessionUser(context.Context, string) (accounts.SessionUser, error)
	ActiveProfiles(context.Context) ([]accounts.Profile, error)
}
type Config struct {
	Root          string
	SocketPath    string
	BridgeSocket  string
	Sessions      SessionResolver
	Logger        *slog.Logger
	CheckStore    func(string) error
	RetryInterval time.Duration
	Media         mediaworker.Worker
}

var ErrStoreUnavailable = errors.New("member-share data volume unavailable")

func RequireOwnedStore(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrStoreUnavailable, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || int(stat.Uid) != os.Geteuid() || info.Mode().Perm() != 0700 {
		return ErrStoreUnavailable
	}
	var fs syscall.Statfs_t
	if err = syscall.Statfs(root, &fs); err != nil || uint64(fs.Type) != 0x9123683e || fs.Flags&unix.ST_RDONLY != 0 {
		return ErrStoreUnavailable
	}
	if _, err = os.Stat(filepath.Join(filepath.Dir(root), ".a-nas-volume.json")); err != nil {
		return ErrStoreUnavailable
	}
	return nil
}

// Proxy forwards the share API to the share service on socketPath; while
// the service cannot be reached, requests answer shares_unavailable.
func Proxy(socketPath string, logger *slog.Logger) http.Handler {
	return serviceproxy.New(socketPath, serviceproxy.Unavailable{Code: "shares_unavailable", Message: "member shares are not available"}, logger)
}

func authenticate(sessions SessionResolver, logger *slog.Logger, next http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get(serviceproxy.SessionHeader)
		user, err := sessions.ResolveSessionUser(r.Context(), token)
		if errors.Is(err, accounts.ErrSessionNotFound) || err == nil && user.UserID == "" {
			httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
			return
		}
		if err != nil {
			logger.WarnContext(r.Context(), "share session lookup failed", "error", err)
			httpjson.WriteError(w, http.StatusServiceUnavailable, "shares_unavailable", "session lookup unavailable")
			return
		}
		if user.MustChangePassword {
			httpjson.WriteError(w, http.StatusForbidden, "password_change_required", "choose a new password before continuing")
			return
		}
		ctx := accounts.WithSessionToken(r.Context(), token)
		ctx = accounts.WithUser(ctx, accounts.User{ID: user.UserID, Username: user.Username, Role: user.Role})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
func listen(socket string) (net.Listener, error) {
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("share socket path is not a socket")
		}
		if err = os.Remove(socket); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(socket, 0660); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}
func Run(parent context.Context, config Config) error {
	if config.Root == "" || config.SocketPath == "" || config.BridgeSocket == "" || config.Sessions == nil {
		return errors.New("share service needs store, API socket, bridge and sessions")
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
	listener, err := listen(config.SocketPath)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(config.SocketPath); _ = listener.Close() }()
	// Open requests drain before the store closes and interrupts the save
	// jobs, as the Product Service drains them before closing its stores
	// (#192): a download in progress still completes.
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	defer cancel()
	var current atomic.Pointer[http.Handler]
	unavailable := sharesapi.New(nil, config.Logger, config.Sessions.ActiveProfiles)
	current.Store(&unavailable)
	server := &http.Server{Handler: authenticate(config.Sessions, config.Logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { (*current.Load()).ServeHTTP(w, r) })), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 1 << 20}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
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
	case err = <-served:
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

// shutdownGrace is how long stopping waits for open requests; the unit's
// TimeoutStopSec leaves room for closing the store after it.
const shutdownGrace = 15 * time.Second

type openedStore struct {
	*shares.Store
	checkBridge func(context.Context) error
}

func openStore(ctx context.Context, config Config) (*openedStore, error) {
	if err := config.CheckStore(config.Root); err != nil {
		return nil, err
	}
	opened, err := os.Lstat(config.Root)
	if err != nil {
		return nil, err
	}
	check := func(context.Context) error {
		if err := config.CheckStore(config.Root); err != nil {
			return err
		}
		current, err := os.Lstat(config.Root)
		if err != nil || !os.SameFile(opened, current) {
			return ErrStoreUnavailable
		}
		return nil
	}
	connect, cancel := context.WithTimeout(ctx, 5*time.Second)
	remote, err := sharefiles.NewRemote(connect, config.BridgeSocket)
	cancel()
	if err != nil {
		return nil, err
	}
	active := func(ctx context.Context, id string) (bool, error) {
		profiles, err := config.Sessions.ActiveProfiles(ctx)
		if err != nil {
			return false, err
		}
		for _, profile := range profiles {
			if profile.ID == id {
				return true, nil
			}
		}
		return false, nil
	}
	authorize := func(ctx context.Context, user shares.User) error {
		session, err := config.Sessions.ResolveSessionUser(ctx, accounts.SessionToken(ctx))
		if err != nil && !errors.Is(err, accounts.ErrSessionNotFound) {
			return err
		}
		if err != nil || session.UserID != user.ID || session.MustChangePassword {
			return shares.ErrUnauthorized
		}
		return nil
	}
	activeAccounts := func(ctx context.Context) (map[string]bool, error) {
		profiles, err := config.Sessions.ActiveProfiles(ctx)
		if err != nil {
			return nil, err
		}
		result := make(map[string]bool, len(profiles))
		for _, profile := range profiles {
			result[profile.ID] = true
		}
		return result, nil
	}
	store, err := shares.Open(filepath.Join(config.Root, "catalog.db"), filepath.Join(config.Root, "content"), shares.Options{Backend: sharefiles.NewBackend(remote), Active: active, ActiveAccounts: activeAccounts, Authorize: authorize, CheckVolume: check, RequireClone: true})
	if err != nil {
		return nil, err
	}
	return &openedStore{Store: store, checkBridge: remote.Check}, nil
}
func supervise(ctx context.Context, config Config, serve func(http.Handler), unavailable http.Handler) {
	ticker := time.NewTicker(config.RetryInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		store, err := openStore(ctx, config)
		if err != nil {
			config.Logger.DebugContext(ctx, "waiting for member-share store or bridge", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				continue
			}
		}
		serve(sharesapi.NewWithPreview(store.Store, config.Logger, config.Sessions.ActiveProfiles, config.Media))
		config.Logger.Info("member-share service ready")
		for {
			select {
			case <-ctx.Done():
				serve(unavailable)
				_ = store.Interrupt("Share service stopped before task completion")
				return
			case <-ticker.C:
			}
			if err = storeCheck(ctx, store, config); err != nil {
				serve(unavailable)
				_ = store.Interrupt("File service or data volume became unavailable; start a new save request")
				config.Logger.WarnContext(ctx, "member-share store unavailable", "error", err)
				break
			}
		}
	}
}

// Domain operations check the pinned root identity before every content write;
// the supervisor additionally takes an unavailable store out of circulation.
func storeCheck(ctx context.Context, store *openedStore, config Config) error {
	if err := config.CheckStore(config.Root); err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := store.checkBridge(bounded); err != nil {
		return err
	}
	return store.CheckStorage(bounded)
}
