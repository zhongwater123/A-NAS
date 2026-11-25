//go:build linux

// Package storeservice runs a service of its own identity that keeps its
// records in a store on the data volume, as the montage service does (ADR
// 0038): it listens on its socket for the Product Service, confirms every
// forwarded session with the Host Agent, serves the store while the data
// volume is there and answers unavailable while it is not. The photo, share
// and family services still run copies of this lifecycle of their own.
package storeservice

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/accountremoval"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/modules"
)

// SessionResolver confirms forwarded sessions and the deletions of
// accounts; on a NAS it is the Host Agent's session lookup.
type SessionResolver interface {
	modules.SessionResolver
	AccountDeletion(context.Context, string) (accountid.AccountDeletion, error)
}

// Config describes a service whose open store is an S.
type Config[S io.Closer] struct {
	// Name names the service in its log: "<name> service ready" once the
	// store is open, "<name> store unavailable" while it is not, which
	// a-nas-check reads.
	Name       string
	Root       string
	SocketPath string
	Sessions   SessionResolver
	// Unavailable is the error code of every request while the store cannot
	// be used.
	Unavailable string
	// Open opens the store in Root once CheckStore accepts it; API serves
	// the open store, and Remove forgets a deleted account in it.
	Open   func(context.Context, string) (S, error)
	API    func(S) http.Handler
	Remove func(S) accountremoval.Remove
	// Upkeep, when set, runs on the open store as it opens and then every
	// UpkeepInterval, beside the requests and the checks of the store. Its
	// context ends when the store is taken out of service, which waits for
	// it before closing the store.
	Upkeep         func(context.Context, S)
	UpkeepInterval time.Duration
	// CheckStore accepts Root as the service's own store on the writable
	// data volume, before the store opens and every RetryInterval while it
	// is served.
	CheckStore    func(string) error
	RetryInterval time.Duration
	Logger        *slog.Logger
	// Authenticate, when set, wraps the API instead of the session check
	// (modules.Authenticate): the shared workspace also takes agent tokens
	// and one-time transfer links (ADR 0040). It must confirm every caller
	// with the Host Agent itself, as the session check does.
	Authenticate func(http.Handler) http.Handler
}

// shutdownGrace is how long stopping waits for open requests.
const shutdownGrace = 15 * time.Second

// Run serves the service on SocketPath until ctx ends.
func Run[S io.Closer](parent context.Context, config Config[S]) error {
	if config.Name == "" || config.Root == "" || config.SocketPath == "" || config.Sessions == nil ||
		config.Unavailable == "" || config.Open == nil || config.API == nil || config.Remove == nil || config.CheckStore == nil {
		return errors.New("a store service needs a name, a store, an API socket, sessions and what to serve")
	}
	if config.Upkeep != nil && config.UpkeepInterval <= 0 {
		return errors.New("a store service's upkeep needs an interval")
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
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
	unavailable := newGeneration(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpjson.WriteError(w, http.StatusServiceUnavailable, config.Unavailable, "the "+config.Name+" store is not available")
	}))
	var current atomic.Pointer[generation]
	current.Store(unavailable)
	served := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A request that took a store as it was taken out of service waits
		// for that, then goes to what serves now.
		for {
			if current.Load().serve(w, r) {
				return
			}
		}
	})
	// A deleted account leaves on the Product Service's request, which
	// carries no session; what to do comes from the Host Agent.
	routes := http.NewServeMux()
	routes.Handle(accountremoval.Path, served)
	authenticate := config.Authenticate
	if authenticate == nil {
		authenticate = func(next http.Handler) http.Handler {
			return modules.Authenticate(config.Sessions, config.Name, config.Unavailable, config.Logger, next)
		}
	}
	routes.Handle("/", authenticate(served))
	server := &http.Server{
		Handler:           routes,
		ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 1 << 20,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	supervised := make(chan struct{})
	go func() {
		defer close(supervised)
		supervise(ctx, config, &current, unavailable)
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

// generation is what serves requests while one store is open, or while
// none is. Taking a store out of service waits for the requests it is
// serving, so that none finds its database closed.
type generation struct {
	handler http.Handler
	mu      sync.Mutex
	active  int
	retired bool
	idle    chan struct{} // closed once retired with no request left
}

func newGeneration(handler http.Handler) *generation {
	return &generation{handler: handler, idle: make(chan struct{})}
}

// serve answers r unless the generation was retired first.
func (g *generation) serve(w http.ResponseWriter, r *http.Request) bool {
	g.mu.Lock()
	if g.retired {
		g.mu.Unlock()
		return false
	}
	g.active++
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.active--
		if g.retired && g.active == 0 {
			close(g.idle)
		}
		g.mu.Unlock()
	}()
	g.handler.ServeHTTP(w, r)
	return true
}

// retire makes serve refuse and waits for the requests in progress.
func (g *generation) retire() {
	g.mu.Lock()
	g.retired = true
	if g.active == 0 {
		close(g.idle)
	}
	g.mu.Unlock()
	<-g.idle
}

// supervise opens the store when it is ready, serves it, and takes it out of
// service when the data volume goes away or turns read-only.
func supervise[S io.Closer](ctx context.Context, config Config[S], current *atomic.Pointer[generation], unavailable *generation) {
	ticker := time.NewTicker(config.RetryInterval)
	defer ticker.Stop()
	// The first failure is a warning, as a store that does not open, such as
	// a database of a newer release, keeps the unit active (a-nas-check).
	warned := false
	for ctx.Err() == nil {
		store, opened, err := open(ctx, config)
		if err != nil {
			if !warned {
				config.Logger.WarnContext(ctx, config.Name+" store unavailable; waiting", "error", err)
				warned = true
			} else {
				config.Logger.DebugContext(ctx, "waiting for the "+config.Name+" store", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				continue
			}
		}
		serving := newGeneration(accountremoval.Route(config.Sessions.AccountDeletion, config.Remove(store), config.Logger,
			config.API(store)))
		current.Store(serving)
		config.Logger.InfoContext(ctx, config.Name+" service ready")
		warned = false
		stopUpkeep := upkeep(ctx, config, store)
		takeOut := func() {
			current.Store(unavailable)
			stopUpkeep()
			serving.retire()
			_ = store.Close()
		}
		for {
			select {
			case <-ctx.Done():
				takeOut()
				return
			case <-ticker.C:
			}
			now, err := os.Lstat(config.Root)
			if err = errors.Join(err, config.CheckStore(config.Root)); err != nil || !os.SameFile(opened, now) {
				takeOut()
				config.Logger.WarnContext(ctx, config.Name+" store unavailable", "error", err)
				break
			}
		}
	}
}

// upkeep runs config.Upkeep on store as it opens and every UpkeepInterval,
// beside the checks of the store, so that upkeep waiting on another service
// never keeps a store in service that has gone. What it returns ends it
// and waits for it, before the store closes.
func upkeep[S io.Closer](ctx context.Context, config Config[S], store S) func() {
	if config.Upkeep == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(config.UpkeepInterval)
		defer ticker.Stop()
		for {
			config.Upkeep(ctx, store)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func open[S io.Closer](ctx context.Context, config Config[S]) (S, os.FileInfo, error) {
	var none S
	if err := config.CheckStore(config.Root); err != nil {
		return none, nil, err
	}
	opened, err := os.Lstat(config.Root)
	if err != nil {
		return none, nil, err
	}
	store, err := config.Open(ctx, config.Root)
	if err != nil {
		return none, nil, err
	}
	return store, opened, nil
}
