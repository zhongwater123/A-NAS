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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/envconfig"
	"github.com/zhongwater123/A-NAS/internal/filebroker/rootbroker"
	linuxhostops "github.com/zhongwater123/A-NAS/internal/hostops/linux"
	"github.com/zhongwater123/A-NAS/internal/hoststate/agent/rootagent"
	"github.com/zhongwater123/A-NAS/internal/hoststate/agent/wire"
	linuxhoststate "github.com/zhongwater123/A-NAS/internal/hoststate/linux"
	"github.com/zhongwater123/A-NAS/internal/sessiondir"
	"github.com/zhongwater123/A-NAS/internal/sessionlookup"
	"github.com/zhongwater123/A-NAS/internal/systemdscope"
	"github.com/zhongwater123/A-NAS/internal/systemlog/journal"
)

var version = "dev"

func main() {
	if len(os.Args) == 3 && os.Args[1] == rootbroker.WorkerArgument {
		if err := rootbroker.RunWorker(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "A-NAS file worker:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) >= 4 && os.Args[1] == rootbroker.ShellGateArgument {
		if err := rootbroker.RunShellGate(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "A-NAS terminal:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 2 && os.Args[1] == rootbroker.ProbeArgument {
		if err := rootbroker.RunProbe(); err != nil {
			fmt.Fprintln(os.Stderr, "A-NAS file worker probe:", err)
			os.Exit(1)
		}
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	// Code without a logger of its own, such as decode guards, logs here too.
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("A-NAS Host Agent stopped", "error", err)
		os.Exit(1)
	}
}

// config is the Host Agent's configuration, read from its environment once.
type config struct {
	Socket     string `env:"ANAS_HOST_AGENT_SOCKET"`
	RuntimeDir string `env:"XDG_RUNTIME_DIR"`
	// Group may open the Host Agent's and the File Broker's sockets.
	Group        string `env:"ANAS_HOST_AGENT_GROUP"`
	DataMount    string `env:"ANAS_DATA_MOUNT" envDefault:"/srv/a-nas/data"`
	SMBInterface string `env:"ANAS_SMB_INTERFACE"`
	StateDir     string `env:"ANAS_STATE_DIR" envDefault:"/var/lib/a-nas"`
	// FileBrokerSocket defaults to the socket beside the Host Agent's.
	FileBrokerSocket string `env:"ANAS_FILE_BROKER_SOCKET"`
	// The AI assistant is not shipped yet (ADR 0024): off unless its
	// sockets are named. With it come the session gate and the executor of
	// its changes, which keeps the copies the members' cards show beside
	// the assistant directories (by default assistant/.changes on the data
	// volume), where only root may read them.
	AssistantSockets       string `env:"ANAS_ASSISTANT_SOCKET_DIR"`
	AssistantChanges       string `env:"ANAS_ASSISTANT_CHANGES_DIR"`
	AssistantTokens        string `env:"ANAS_ASSISTANT_TOKEN_DIR" envDefault:"/etc/a-nas-assistant/tokens"`
	AssistantChangesSocket string `env:"ANAS_ASSISTANT_CHANGES_SOCKET" envDefault:"/run/a-nas/assistant-changes.sock"`
	// Without the photo service's group the photo service is not installed
	// and its session lookup is off; the default is the service's user.
	PhotoSessionGroup  string `env:"ANAS_PHOTO_SESSION_GROUP"`
	PhotoSessionSocket string `env:"ANAS_PHOTO_SESSION_SOCKET" envDefault:"/run/a-nas-sessions/photos.sock"`
	// PhotoAssistantLookup=1 looks accounts up by UID for the photo
	// service's socket for members' AI assistants, which is not shipped yet.
	PhotoAssistantLookup string `env:"ANAS_PHOTO_ASSISTANT_LOOKUP"`
	ShareSessionGroup    string `env:"ANAS_SHARE_SESSION_GROUP"`
	ShareSessionSocket   string `env:"ANAS_SHARE_SESSION_SOCKET" envDefault:"/run/a-nas-share-sessions/shares.sock"`
}

func loadConfig(environment map[string]string) (config, error) {
	c, err := envconfig.Parse[config](environment)
	if err != nil {
		return c, err
	}
	if c.Socket, err = wire.SocketPath(c.Socket, c.RuntimeDir); err != nil {
		return c, err
	}
	if c.FileBrokerSocket == "" {
		c.FileBrokerSocket = filepath.Join(filepath.Dir(c.Socket), "file-broker.sock")
	}
	if c.AssistantSockets != "" && c.AssistantChanges == "" {
		c.AssistantChanges = filepath.Join(c.DataMount, "assistant", ".changes")
	}
	if c.AssistantSockets == "" {
		c.AssistantChanges = ""
	}
	if c.PhotoSessionGroup == "" {
		c.PhotoSessionGroup = accountid.PhotoServiceUser
	}
	if c.ShareSessionGroup == "" {
		c.ShareSessionGroup = accountid.ShareServiceUser
	}
	return c, nil
}

func run(logger *slog.Logger) error {
	if os.Geteuid() != 0 {
		return errors.New("A-NAS Host Agent must run as root")
	}
	config, err := loadConfig(envconfig.Environment())
	if err != nil {
		return err
	}
	socketPath := config.Socket
	listener, err := listen(socketPath, config.Group)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()
	reader := linuxhoststate.New()
	// State reads only show SMART readings and refresh them in the
	// background; the first read then already has them.
	go func() {
		if err := reader.RefreshSMART(context.Background()); err != nil {
			logger.Warn("initial SMART read failed", "error", err)
		}
	}()
	mountPoint := config.DataMount
	executor := linuxhostops.NewExecutor(linuxhostops.HostStateResolver{Reader: reader}, nil, linuxhostops.Options{
		MountPoint:   mountPoint,
		SMBInterface: config.SMBInterface,
		Logger:       logger,
	})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var reconcileFailed atomic.Bool
	server := &http.Server{
		Handler: rootagent.NewOperationsHandler(rootagent.Services{
			Reader: reader, Volume: executor, Credentials: executor, Identities: executor, Viewing: executor, Apps: executor, Snapshots: executor,
			SystemLogs: journal.New(), ReconcileFailed: reconcileFailed.Load,
		}, logger),
		ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 1 << 20,
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	sessions := sessiondir.New(filepath.Join(config.StateDir, "control.db"))
	defer func() { _ = sessions.Close() }()
	assistantChanges := config.AssistantChanges
	// File workers and terminal shells run in scopes of their own, so they
	// share neither the Host Agent's limits nor its name in the journal.
	// Without systemd, as in development, they stay in the Host Agent's.
	var scopes rootbroker.Placer
	if placer, err := systemdscope.New(ctx); err != nil {
		logger.Warn("file workers and terminals run in the Host Agent's cgroup", "error", err)
	} else {
		defer placer.Close()
		scopes = placer
	}
	broker, err := rootbroker.NewServer(rootbroker.Config{
		Sessions: sessions, VolumeRoot: mountPoint, VolumeReady: executor.DataVolumeReady, Logger: logger,
		AssistantSockets: config.AssistantSockets,
		AssistantTokens:  config.AssistantTokens,
		AssistantChanges: assistantChanges,
		Accounts:         sessions,
		Scopes:           scopes,
	})
	if err != nil {
		return err
	}
	defer broker.Close()
	// Web files and the terminal run as the signed-in user. A sandbox that
	// took the capabilities for that (issue #38) leaves the rest of the Host
	// Agent working, so it is reported, not fatal.
	if err := broker.VerifyIdentitySwitch(); err != nil {
		logger.Error("file broker cannot switch to user identities; Web files and the terminal are unavailable", "error", err)
	} else {
		logger.Info("file broker identity switch verified")
	}
	brokerSocket := config.FileBrokerSocket
	brokerListener, err := listen(brokerSocket, config.Group)
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
	if assistantChanges != "" {
		stopChanges, err := serveAssistantChanges(config.AssistantChangesSocket, broker, logger)
		if err != nil {
			return err
		}
		defer stopChanges()
	}
	stopSessionLookup, err := serveSessionLookup(config, sessions, logger)
	if err != nil {
		return err
	}
	defer stopSessionLookup()
	stopShareLookup, err := serveShareSessionLookup(config, sessions, logger)
	if err != nil {
		return err
	}
	defer stopShareLookup()
	// Serving waits for nothing on the data volume: one that cannot be
	// reconciled, or a long walk over a large space, leaves Web files, the
	// terminal and the logs working (#166).
	go keepDataVolume(ctx, executor, &reconcileFailed, logger)
	go keepViewing(ctx, executor, logger)
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

// serveAssistantChanges answers the assistants' MCP tool. The socket is open
// to everyone but sits in /run/a-nas, which members cannot enter; the units
// bind it into each assistant's sandbox, and the member is the caller's UID.
func serveAssistantChanges(socketPath string, broker *rootbroker.Server, logger *slog.Logger) (func(), error) {
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("listen for the assistants' changes: %w", err)
	}
	if err := os.Chmod(socketPath, 0o666); err != nil {
		_ = listener.Close()
		return nil, err
	}
	go func() {
		if err := broker.ServeAssistantChanges(listener); err != nil {
			logger.Error("assistant changes stopped", "error", err)
		}
	}()
	logger.Info("assistant changes listening", "socket", socketPath)
	return func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}, nil
}

// serveSessionLookup lets the photo service confirm who stands behind a
// session token (ADR 0011). Only the photo service's group can reach the
// socket; without that group the photo service is not installed and nothing
// is served.
func serveSessionLookup(config config, sessions *sessiondir.Directory, logger *slog.Logger) (func(), error) {
	group := config.PhotoSessionGroup
	if _, err := user.LookupGroup(group); err != nil {
		logger.Info("photo service is not installed; session lookup is off", "group", group)
		return func() {}, nil
	}
	socketPath := config.PhotoSessionSocket
	listener, err := listen(socketPath, group)
	if err != nil {
		return nil, fmt.Errorf("listen for photo session lookups: %w", err)
	}
	// Looking accounts up by UID serves the photo service's socket for members' AI
	// assistants (ADR 0024), which is not shipped yet; it stays off until named.
	var accountsByUID sessionlookup.AccountResolver
	if config.PhotoAssistantLookup == "1" {
		accountsByUID = sessions
	}
	server := &http.Server{
		Handler:           sessionlookup.NewHandler(sessions, accountsByUID, logger),
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

// keepDataVolume reconciles the data volume, its space permissions, the
// identity manifest on it and the Samba configuration for it, once the Host
// Agent serves, then every 15 minutes reapplies the space ACL layout so manual
// chmod/setfacl changes on space roots do not persist silently. A failure,
// such as a volume Btrfs turned read-only, is logged and reported in the host
// state through failed, and the next round reconciles everything again; the
// Host Agent keeps serving either way.
func keepDataVolume(ctx context.Context, executor *linuxhostops.Executor, failed *atomic.Bool, logger *slog.Logger) {
	reconcile := func(everything bool) {
		var repaired []string
		var err error
		if everything {
			repaired, err = executor.ReconcileDataVolume(ctx)
		} else {
			repaired, err = executor.RepairDataVolumePermissions(ctx)
		}
		if ctx.Err() != nil {
			return
		}
		switch {
		case err != nil:
			logger.ErrorContext(ctx, "data-volume reconciliation failed", "error", err)
		case everything:
			logger.InfoContext(ctx, "data volume reconciled")
		}
		failed.Store(err != nil)
		logRepairs(logger, repaired)
		ensureManagedStores(ctx, executor, logger)
		continueConversion(ctx, executor, logger)
	}
	reconcile(true)
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile(failed.Load())
		}
	}
}

// continueConversion resumes converting the data volume to its layout after
// a restart or shutdown cancelled the balance (ADR 0025).
func continueConversion(ctx context.Context, executor *linuxhostops.Executor, logger *slog.Logger) {
	if _, err := executor.ReconcileConversion(ctx); err != nil {
		logger.ErrorContext(ctx, "data-volume conversion check failed", "error", err)
	}
}

// ensureManagedStores keeps isolated service stores in shape. A failure, such
// as another account holding a service UID, only leaves that service
// unavailable; the Host Agent keeps serving everything else.
func ensureManagedStores(ctx context.Context, executor *linuxhostops.Executor, logger *slog.Logger) {
	shareRepairs, shareErr := executor.EnsureSharesStore(ctx)
	if shareErr != nil {
		logger.ErrorContext(ctx, "share store repair failed", "error", shareErr)
	}
	logRepairs(logger, shareRepairs)
	repaired, err := executor.EnsurePhotosStore(ctx)
	if err != nil {
		logger.ErrorContext(ctx, "photo store repair failed", "error", err)
	}
	logRepairs(logger, repaired)
}

// keepViewing revokes Administrative Viewing Mode grants on time: those that
// expired while the Host Agent was stopped once it serves, then every minute.
// Revoking walks the whole private space, which takes minutes on a large one,
// so nothing waits for it; the Product Service already refuses an expired
// grant, and the ACL entries are the second line.
func keepViewing(ctx context.Context, executor *linuxhostops.Executor, logger *slog.Logger) {
	expireViewing(ctx, executor, logger)
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

func serveShareSessionLookup(config config, sessions *sessiondir.Directory, logger *slog.Logger) (func(), error) {
	group := config.ShareSessionGroup
	if _, err := user.LookupGroup(group); err != nil {
		logger.Info("share service is not installed; lookup is off")
		return func() {}, nil
	}
	socket := config.ShareSessionSocket
	listener, err := listen(socket, group)
	if err != nil {
		return nil, err
	}
	server := &http.Server{Handler: sessionlookup.NewHandler(sessions, nil, logger), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("share session lookup stopped", "error", err)
		}
	}()
	return func() { _ = server.Close(); _ = os.Remove(socket) }, nil
}
