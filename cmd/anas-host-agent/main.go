package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/filebroker"
	linuxhostops "github.com/zhongwater123/A-NAS/internal/hostops/linux"
	"github.com/zhongwater123/A-NAS/internal/hoststate/agent"
	linuxhoststate "github.com/zhongwater123/A-NAS/internal/hoststate/linux"
	"github.com/zhongwater123/A-NAS/internal/sessionlookup"
)

var version = "dev"

func main() {
	if len(os.Args) == 3 && os.Args[1] == filebroker.WorkerArgument {
		if err := filebroker.RunWorker(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "A-NAS file worker:", err)
			os.Exit(1)
		}
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("A-NAS Host Agent stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	if os.Geteuid() != 0 {
		return errors.New("A-NAS Host Agent must run as root")
	}
	socketPath, err := agent.SocketPath()
	if err != nil {
		return err
	}
	listener, err := listen(socketPath, strings.TrimSpace(os.Getenv("ANAS_HOST_AGENT_GROUP")))
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()
	reader := linuxhoststate.New()
	mountPoint := environment("ANAS_DATA_MOUNT", "/srv/a-nas/data")
	executor := linuxhostops.NewExecutor(linuxhostops.HostStateResolver{Reader: reader}, nil, linuxhostops.Options{
		MountPoint:   mountPoint,
		SMBInterface: strings.TrimSpace(os.Getenv("ANAS_SMB_INTERFACE")),
	})
	repaired, err := executor.ReconcileDataVolume(context.Background())
	if err != nil {
		return err
	}
	logRepairs(logger, repaired)
	expireViewing(context.Background(), executor, logger)
	server := &http.Server{
		Handler: agent.NewOperationsHandler(agent.Services{
			Reader: reader, Volume: executor, Credentials: executor, Identities: executor, Viewing: executor, Apps: executor, Snapshots: executor,
		}, logger),
		ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 1 << 20,
	}
	sessions := accounts.NewSessionDirectory(filepath.Join(environment("ANAS_STATE_DIR", "/var/lib/a-nas"), "control.db"))
	defer sessions.Close()
	broker, err := filebroker.NewServer(filebroker.Config{
		Sessions: sessions, VolumeRoot: mountPoint, VolumeReady: executor.DataVolumeReady, Logger: logger,
	})
	if err != nil {
		return err
	}
	defer broker.Close()
	brokerSocket := environment("ANAS_FILE_BROKER_SOCKET", filepath.Join(filepath.Dir(socketPath), "file-broker.sock"))
	brokerListener, err := listen(brokerSocket, strings.TrimSpace(os.Getenv("ANAS_HOST_AGENT_GROUP")))
	if err != nil {
		return err
	}
	defer func() {
		_ = brokerListener.Close()
		_ = os.Remove(brokerSocket)
	}()
	go func() {
		if err := broker.Serve(brokerListener); err != nil {
			logger.Error("file broker stopped", "error", err)
		}
	}()
	stopSessionLookup, err := serveSessionLookup(sessions, logger)
	if err != nil {
		return err
	}
	defer stopSessionLookup()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go repairPermissionsPeriodically(ctx, executor, logger)
	go expireViewingPeriodically(ctx, executor, logger)
	serveError := make(chan error, 1)
	go func() {
		logger.Info("A-NAS Host Agent listening", "socket", socketPath, "file_broker", brokerSocket, "version", version)
		serveError <- server.Serve(listener)
	}()
	select {
	case err := <-serveError:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("A-NAS Host Agent shutting down")
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

// serveSessionLookup lets the photo service confirm who stands behind a
// session token (ADR 0011). Only the photo service's group can reach the
// socket; without that group the photo service is not installed and nothing
// is served.
func serveSessionLookup(sessions *accounts.SessionDirectory, logger *slog.Logger) (func(), error) {
	group := environment("ANAS_PHOTO_SESSION_GROUP", accounts.PhotoServiceUser)
	if _, err := user.LookupGroup(group); err != nil {
		logger.Info("photo service is not installed; session lookup is off", "group", group)
		return func() {}, nil
	}
	socketPath := environment("ANAS_PHOTO_SESSION_SOCKET", "/run/a-nas-sessions/photos.sock")
	listener, err := listen(socketPath, group)
	if err != nil {
		return nil, fmt.Errorf("listen for photo session lookups: %w", err)
	}
	server := &http.Server{
		Handler:           sessionlookup.NewHandler(sessions, logger),
		ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10,
	}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("photo session lookup stopped", "error", err)
		}
	}()
	logger.Info("photo session lookup listening", "socket", socketPath)
	return func() {
		_ = server.Close()
		_ = os.Remove(socketPath)
	}, nil
}

// repairPermissionsPeriodically reapplies the space ACL layout so manual
// chmod/setfacl changes on space roots do not persist silently.
func repairPermissionsPeriodically(ctx context.Context, executor *linuxhostops.Executor, logger *slog.Logger) {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			repaired, err := executor.RepairDataVolumePermissions(ctx)
			if err != nil {
				logger.ErrorContext(ctx, "data-volume permission repair failed", "error", err)
			}
			logRepairs(logger, repaired)
		}
	}
}

// expireViewingPeriodically revokes Administrative Viewing Mode grants on
// time; grants that expired while the Host Agent was stopped are revoked at
// startup.
func expireViewingPeriodically(ctx context.Context, executor *linuxhostops.Executor, logger *slog.Logger) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			expireViewing(ctx, executor, logger)
		}
	}
}

func expireViewing(ctx context.Context, executor *linuxhostops.Executor, logger *slog.Logger) {
	revoked, err := executor.ExpireViewing(ctx)
	if err != nil {
		logger.ErrorContext(ctx, "revoking expired viewing grants failed", "error", err)
	}
	for _, id := range revoked {
		logger.Info("viewing grant expired and revoked", "grant", id)
	}
}

func logRepairs(logger *slog.Logger, repaired []string) {
	for _, path := range repaired {
		logger.Warn("repaired drifted data-volume permissions", "path", path)
	}
}

func listen(socketPath, groupName string) (net.Listener, error) {
	directory := filepath.Dir(socketPath)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return nil, err
	}
	if err := os.Chmod(directory, 0o750); err != nil {
		return nil, err
	}
	gid := -1
	if groupName != "" {
		group, err := user.LookupGroup(groupName)
		if err != nil {
			return nil, err
		}
		gid, err = strconv.Atoi(group.Gid)
		if err != nil {
			return nil, err
		}
		if err := os.Chown(directory, 0, gid); err != nil {
			return nil, err
		}
	}
	if info, err := os.Lstat(socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("host agent socket path exists and is not a socket")
		}
		connection, dialErr := net.DialTimeout("unix", socketPath, 200*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			return nil, errors.New("host agent socket is already in use")
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
	mode := os.FileMode(0o600)
	if gid >= 0 {
		if err := os.Chown(socketPath, 0, gid); err != nil {
			_ = listener.Close()
			_ = os.Remove(socketPath)
			return nil, err
		}
		mode = 0o660
	}
	if err := os.Chmod(socketPath, mode); err != nil {
		_ = listener.Close()
		_ = os.Remove(socketPath)
		return nil, err
	}
	return listener, nil
}

func environment(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
