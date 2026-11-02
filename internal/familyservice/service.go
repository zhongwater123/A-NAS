//go:build linux

package familyservice

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
	"github.com/zhongwater123/A-NAS/internal/family"
	"github.com/zhongwater123/A-NAS/internal/familyapi"
	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
)

// SessionResolver confirms sessions and lists the accounts in use; on a NAS
// it is the Host Agent's session lookup.
type SessionResolver interface {
	ResolveSessionUser(context.Context, string) (accounts.SessionUser, error)
	ActiveProfiles(context.Context) ([]accounts.Profile, error)
}

type Config struct {
	Root       string
	SocketPath string
	Sessions   SessionResolver
	// Photos is the photo API, reached through the photo service's socket.
	Photos        http.Handler
	Media         ffmpegworker.Worker
	Logger        *slog.Logger
	CheckStore    func(string) error
	RetryInterval time.Duration
}

var ErrStoreUnavailable = errors.New("family store on the data volume is unavailable")

// RequireOwnedStore accepts root only as this service's own private
// directory on the writable, verified data volume, so the family's records
// are never written to the system disk (ADR 0029).
func RequireOwnedStore(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrStoreUnavailable, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || int(stat.Uid) != os.Geteuid() || info.Mode().Perm() != 0o700 {
		return ErrStoreUnavailable
	}
	var fs syscall.Statfs_t
	if err = syscall.Statfs(root, &fs); err != nil || uint64(fs.Type) != unix.BTRFS_SUPER_MAGIC || fs.Flags&unix.ST_RDONLY != 0 {
		return ErrStoreUnavailable
	}
	if _, err = os.Stat(filepath.Join(filepath.Dir(root), ".a-nas-volume.json")); err != nil {
		return ErrStoreUnavailable
	}
	return nil
}

// Proxy forwards the family API to the family service on socketPath; while
// the service cannot be reached, requests answer family_unavailable.
func Proxy(socketPath string, logger *slog.Logger) http.Handler {
	return serviceproxy.New(socketPath, serviceproxy.Unavailable{
		Code: "family_unavailable", Message: "the family center is not available",
	}, logger)
}

// PhotoAPI reaches the photo service on socketPath for Photos.
func PhotoAPI(socketPath string, logger *slog.Logger) http.Handler {
	return serviceproxy.New(socketPath, serviceproxy.Unavailable{
		Code: "photos_unavailable", Message: "the photo library is not available",
	}, logger)
}

// authenticate trusts only the Host Agent's answer about the forwarded
// session, never the Product Service.
func authenticate(sessions SessionResolver, logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get(serviceproxy.SessionHeader)
		user, err := sessions.ResolveSessionUser(r.Context(), token)
		if errors.Is(err, accounts.ErrSessionNotFound) || err == nil && user.UserID == "" {
			httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
			return
		}
		if err != nil {
			logger.WarnContext(r.Context(), "family session lookup failed", "error", err)
			httpjson.WriteError(w, http.StatusServiceUnavailable, "family_unavailable", "session lookup unavailable")
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
			return nil, errors.New("family socket path is not a socket")
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
	// The Product Service reaches the socket through this service's group;
	// the runtime directory keeps everyone else out.
	if err = os.Chmod(socket, 0o660); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}

// shutdownGrace is how long stopping waits for open requests.
const shutdownGrace = 15 * time.Second

// purgeInterval is how often the trash is emptied of what has expired.
const purgeInterval = time.Hour

// Run serves the family API on SocketPath until ctx ends. While the store
// cannot be opened, as when the data volume is offline, requests answer
// family_unavailable.
func Run(parent context.Context, config Config) error {
	if config.Root == "" || config.SocketPath == "" || config.Sessions == nil || config.Photos == nil {
		return errors.New("family service needs a store, an API socket, sessions and photos")
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
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	defer cancel()
	var current atomic.Pointer[http.Handler]
	unavailable := familyapi.New(nil, config.Logger, nil)
	current.Store(&unavailable)
	server := &http.Server{
		Handler: authenticate(config.Sessions, config.Logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			(*current.Load()).ServeHTTP(w, r)
		})),
		ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 1 << 20,
	}
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

// Accounts turns the active profiles into the family store's account lookup.
func Accounts(profiles func(context.Context) ([]accounts.Profile, error)) func(context.Context) (map[string]family.Account, error) {
	return func(ctx context.Context) (map[string]family.Account, error) {
		list, err := profiles(ctx)
		if err != nil {
			return nil, err
		}
		found := make(map[string]family.Account, len(list))
		for _, profile := range list {
			found[profile.ID] = family.Account{ID: profile.ID, Username: profile.Username}
		}
		return found, nil
	}
}

// supervise opens the store when it is ready, serves it, and takes it out of
// service when the data volume goes away.
func supervise(ctx context.Context, config Config, serve func(http.Handler), unavailable http.Handler) {
	ticker := time.NewTicker(config.RetryInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		store, opened, err := open(config)
		if err != nil {
			config.Logger.DebugContext(ctx, "waiting for the family store", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				continue
			}
		}
		serve(familyapi.New(store, config.Logger, config.Media))
		config.Logger.Info("family service ready")
		purged := time.Time{}
		for {
			if time.Since(purged) >= purgeInterval {
				if err := store.Purge(ctx); err != nil {
					config.Logger.WarnContext(ctx, "family trash purge failed", "error", err)
				}
				purged = time.Now()
			}
			select {
			case <-ctx.Done():
				serve(unavailable)
				_ = store.Close()
				return
			case <-ticker.C:
			}
			current, err := os.Lstat(config.Root)
			if err = errors.Join(err, config.CheckStore(config.Root)); err != nil || !os.SameFile(opened, current) {
				serve(unavailable)
				_ = store.Close()
				config.Logger.WarnContext(ctx, "family store unavailable", "error", err)
				break
			}
		}
	}
}

func open(config Config) (*family.Store, os.FileInfo, error) {
	if err := config.CheckStore(config.Root); err != nil {
		return nil, nil, err
	}
	opened, err := os.Lstat(config.Root)
	if err != nil {
		return nil, nil, err
	}
	store, err := family.Open(config.Root, family.Options{
		Accounts: Accounts(config.Sessions.ActiveProfiles),
		Photos:   Photos{API: config.Photos},
	})
	if err != nil {
		return nil, nil, err
	}
	return store, opened, nil
}
