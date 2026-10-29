// Command anas-container-agent is the only A-NAS process with access to the
// Docker Engine socket. It runs as a dedicated system user and serves the typed
// container operations to the Product Service over a Unix socket that only
// members of its group can open.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/moby/moby/client"

	"github.com/zhongwater123/A-NAS/internal/appstore"
	appstoreagent "github.com/zhongwater123/A-NAS/internal/appstore/agent"
	"github.com/zhongwater123/A-NAS/internal/appstore/engine"
	"github.com/zhongwater123/A-NAS/internal/containers/agent"
	"github.com/zhongwater123/A-NAS/internal/containers/docker"
	"github.com/zhongwater123/A-NAS/internal/envconfig"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("A-NAS container agent stopped", "error", err)
		os.Exit(1)
	}
}

// config is the container agent's configuration, read from its environment
// once. systemd sets STATE_DIRECTORY for StateDirectory=; Compose files and
// the Docker CLI configuration live there. App data lives on the data volume
// beside the spaces; shared mounts resolve inside the Shared folder, never at
// the volume root (ADR 0008).
type config struct {
	Socket         string `env:"ANAS_CONTAINER_AGENT_SOCKET"`
	StateDir       string `env:"STATE_DIRECTORY" envDefault:"/var/lib/a-nas-container"`
	AppDataRoot    string `env:"ANAS_APP_DATA_ROOT" envDefault:"/srv/a-nas/data/apps"`
	SharedDataRoot string `env:"ANAS_SHARED_DATA_ROOT" envDefault:"/srv/a-nas/data/spaces/shared"`
	TZ             string `env:"TZ" envDefault:"Etc/UTC"`
}

func run(logger *slog.Logger) error {
	config, err := envconfig.Parse[config](envconfig.Environment())
	if err != nil {
		return err
	}
	socketPath, err := agent.SocketPath(config.Socket)
	if err != nil {
		return err
	}
	manager, err := docker.New()
	if err != nil {
		return err
	}
	defer func() { _ = manager.Close() }()

	apps, err := configuredAppStore(config)
	if err != nil {
		return err
	}
	// An install this agent was stopped in is rolled back in the background.
	if err := apps.Recover(); err != nil {
		logger.Error("some app job records could not be read", "error", err)
	}
	// An install still running on shutdown is cancelled and rolled back
	// before the agent exits; an uninstall is let finish.
	defer apps.Stop()

	listener, err := listen(socketPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()

	server := &http.Server{
		Handler:           route(agent.NewHandler(manager, logger), appstoreagent.NewHandler(apps, appstoreagent.SystemIdentities{}, logger)),
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		// Stop and restart wait up to ten seconds for the container to exit.
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  30 * time.Second,
		ErrorLog:     slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveError := make(chan error, 1)
	go func() {
		logger.Info("A-NAS container agent listening", "socket", socketPath, "version", version)
		serveError <- server.Serve(listener)
	}()

	select {
	case err := <-serveError:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("A-NAS container agent shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		err := <-serveError
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func route(containerHandler, appHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if appstoreagent.Matches(r.URL.Path) {
			appHandler.ServeHTTP(w, r)
			return
		}
		containerHandler.ServeHTTP(w, r)
	})
}

// configuredAppStore applies the configured install policy.
func configuredAppStore(config config) (*engine.Store, error) {
	entries, err := appstore.Catalog()
	if err != nil {
		return nil, err
	}
	stateDir := config.StateDir
	policy := appstore.Policy{
		AppDataRoot:   config.AppDataRoot,
		DataRoot:      config.SharedDataRoot,
		TZ:            config.TZ,
		ReservedPorts: []uint16{8080},
	}
	for _, root := range []string{policy.AppDataRoot, policy.DataRoot, stateDir} {
		if !filepath.IsAbs(root) {
			return nil, errors.New("app data roots and state directory must be absolute paths")
		}
	}
	engineClient, err := client.New(client.FromEnv)
	if err != nil {
		return nil, err
	}
	runner := engine.CLIRunner{Environment: []string{"DOCKER_CONFIG=" + filepath.Join(stateDir, "docker-config")}}
	return engine.New(entries, policy, stateDir, engine.DockerInspector{Client: engineClient}, runner), nil
}

// listen creates the socket with group read/write so that only members of the
// agent's group (the Product Service user) can connect; systemd's
// RuntimeDirectory provides the 0750 parent directory in production.
func listen(socketPath string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o750); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("container agent socket path exists and is not a socket")
		}
		if connection, dialErr := net.DialTimeout("unix", socketPath, 200*time.Millisecond); dialErr == nil {
			_ = connection.Close()
			return nil, errors.New("container agent socket is already in use")
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
		_ = os.Remove(socketPath)
		return nil, err
	}
	return listener, nil
}
