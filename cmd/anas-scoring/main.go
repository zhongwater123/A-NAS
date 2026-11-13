// Command anas-scoring is the photo scoring module (docs/specs/photo-scoring.md,
// ADR 0037). It runs as a-nas-scoring under anas-module-scoring.service, which
// hides the data volume but for its store and, read-only, the Shared folder
// 照片打分, and takes its API socket from anas-module-scoring.socket; the
// Product Service forwards /api/v1/modules/scoring to it.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zhongwater123/A-NAS/internal/envconfig"
	"github.com/zhongwater123/A-NAS/internal/filesystem"
	"github.com/zhongwater123/A-NAS/internal/modules"
	"github.com/zhongwater123/A-NAS/internal/scoring"
	"github.com/zhongwater123/A-NAS/internal/scoringapi"
	"github.com/zhongwater123/A-NAS/internal/sessionlookup"
)

var version = "dev"

type config struct {
	// Store and Photos are what the unit leaves visible of the data volume.
	Store         string `env:"ANAS_SCORING_STORE" envDefault:"/srv/a-nas/data/module-scoring"`
	Photos        string `env:"ANAS_SCORING_PHOTOS" envDefault:"/srv/a-nas/data/spaces/shared/照片打分"`
	SessionSocket string `env:"ANAS_MODULE_SESSION_SOCKET" envDefault:"/run/a-nas-module-sessions/sessions.sock"`
	// Socket is only for tests; under systemd the socket unit hands it.
	Socket string `env:"ANAS_SCORING_SOCKET"`
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("photo scoring stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	if os.Geteuid() == 0 {
		return errors.New("photo scoring must run as its own account, never root")
	}
	c, err := envconfig.Parse[config](envconfig.Environment())
	if err != nil {
		return err
	}
	var problems []error
	for name, value := range map[string]*string{
		"ANAS_SCORING_STORE": &c.Store, "ANAS_SCORING_PHOTOS": &c.Photos,
		"ANAS_MODULE_SESSION_SOCKET": &c.SessionSocket, "ANAS_SCORING_SOCKET": &c.Socket,
	} {
		cleaned, err := envconfig.AbsolutePath(name, *value)
		problems = append(problems, err)
		*value = cleaned
	}
	if err := errors.Join(problems...); err != nil {
		return err
	}
	// Without the store the unit started before the data volume was
	// mounted; exit so that systemd restarts the unit with the mount.
	if err := modules.RequireOwnedStore(c.Store); err != nil {
		return err
	}
	store, err := scoring.Open(c.Store, scoring.Options{
		PhotosDir: c.Photos, Photos: filesystem.NewRootFileSystem(c.Photos, false), Logger: logger,
	})
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	listener, err := modules.Listener(c.Socket)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, lost := context.WithCancelCause(ctx)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := modules.RequireOwnedStore(c.Store); err != nil {
					lost(err)
					return
				}
			}
		}
	}()
	server := &http.Server{
		Handler:           scoringapi.Start(ctx, store, sessionlookup.NewClient(c.SessionSocket), version, logger),
		ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20,
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	logger.Info("photo scoring started", "version", version)
	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}
	return nil
}
