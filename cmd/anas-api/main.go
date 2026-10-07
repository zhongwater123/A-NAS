package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/appid"
	"github.com/zhongwater123/A-NAS/internal/appstore"
	appstoreagent "github.com/zhongwater123/A-NAS/internal/appstore/agent"
	fakeappstore "github.com/zhongwater123/A-NAS/internal/appstore/fake"
	"github.com/zhongwater123/A-NAS/internal/appstoreapi"
	"github.com/zhongwater123/A-NAS/internal/containers"
	containeragent "github.com/zhongwater123/A-NAS/internal/containers/agent"
	fakecontainers "github.com/zhongwater123/A-NAS/internal/containers/fake"
	"github.com/zhongwater123/A-NAS/internal/containersapi"
	"github.com/zhongwater123/A-NAS/internal/filebroker"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/hoststate/agent"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/storage"
	"github.com/zhongwater123/A-NAS/internal/terminal"
	"github.com/zhongwater123/A-NAS/internal/webui"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("A-NAS API stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	address := environment("ANAS_HTTP_ADDR", "127.0.0.1:8080")
	stateDirectory, err := stateDirectory()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
		return err
	}
	reader, dataSource, operations, live, err := configuredServices()
	if err != nil {
		return err
	}
	accountStore, err := accounts.OpenSQLite(filepath.Join(stateDirectory, "control.db"))
	if err != nil {
		return err
	}
	defer accountStore.Close()
	fileStore, err := files.OpenSQLite(filepath.Join(stateDirectory, "files.db"))
	if err != nil {
		return err
	}
	defer fileStore.Close()
	storageStore, err := storage.OpenSQLite(filepath.Join(stateDirectory, "storage.db"))
	if err != nil {
		return err
	}
	defer storageStore.Close()
	accountService := accounts.NewService(accountStore, operations, accounts.Options{})
	storageService, err := storage.OpenService(reader, operations, storage.Options{Store: storageStore})
	if err != nil {
		return err
	}
	volumeRoot := strings.TrimSpace(os.Getenv("ANAS_DATA_MOUNT"))
	if volumeRoot == "" {
		if live {
			volumeRoot = "/srv/a-nas/data"
		} else {
			volumeRoot = filepath.Join(stateDirectory, "development-volume")
		}
	}
	fileOptions := files.Options{AllowUnverifiedVolume: !live}
	terminalConfig := terminal.Config{}
	if live {
		fileOptions.VolumeGuard = files.BtrfsVolumeGuard{ExpectedFilesystemUUID: func(ctx context.Context) (string, error) {
			volumes, err := storageService.ListVolumes(ctx)
			if err != nil || len(volumes) != 1 || volumes[0].State != storage.VolumeStateAvailable {
				return "", err
			}
			return volumes[0].FilesystemUUID, nil
		}}
		fileOptions.SnapshotBackend = operations
		brokerSocket, err := fileBrokerSocket()
		if err != nil {
			return err
		}
		broker := filebroker.NewClient(brokerSocket)
		fileOptions.FileSystem = broker
		// Terminal shells run as the signed-in administrator, not as a-nas.
		terminalConfig.Spawner = broker
	} else if err := os.MkdirAll(volumeRoot, 0o700); err != nil {
		return err
	}
	fileService := files.NewService(fileStore, volumeRoot, accountService, fileOptions)
	terminalEnabled, err := configuredTerminal()
	if err != nil {
		return err
	}
	terminalConfig.Enabled = terminalEnabled
	terminals := terminal.New(terminalConfig, logger)

	containerManager, appStore, containerSource, err := configuredContainers(dataSource)
	if err != nil {
		return err
	}

	appOptions := appstoreapi.Options{Host: appstoreapi.DevelopmentHost{}, Logger: logger}
	if live {
		host, ok := operations.(appid.Host)
		if !ok {
			return errors.New("host agent client cannot prepare apps")
		}
		appOptions.Host = host
		guard := fileOptions.VolumeGuard
		appOptions.VolumeReady = func(ctx context.Context) error { return guard.Check(ctx, volumeRoot, true) }
	}

	apiHandler := httpapi.NewProduct(httpapi.ProductDependencies{
		Reader: reader, DataSource: dataSource, ProductVersion: version,
		Accounts: accountService, Files: fileService, Storage: storageService,
		Terminal:   terminals,
		Containers: containersapi.New(containerManager, containerSource, logger),
		Apps:       appstoreapi.New(appStore, appstoreapi.DataSource(containerSource), appOptions),
		Logger:     logger,
	})
	handler, err := webui.New(apiHandler)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go syncIdentities(ctx, accountService, logger)
	go runDirectoryReconciliation(ctx, accountService, fileService, logger)
	listenError := make(chan error, 1)
	go func() {
		logger.Info("A-NAS API listening", "address", address, "version", version, "data_source", dataSource, "terminal", terminalEnabled, "containers", containerManager != nil)
		listenError <- server.ListenAndServe()
	}()
	select {
	case err := <-listenError:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("A-NAS API shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		if err := terminals.Shutdown(shutdownCtx); err != nil {
			return err
		}
		err := <-listenError
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// syncIdentities converges host accounts on the control plane at startup, so
// accounts created before ADR 0008 receive their A-NAS UID and groups.
func syncIdentities(ctx context.Context, accountService *accounts.Service, logger *slog.Logger) {
	syncCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := accountService.SyncIdentities(syncCtx); err != nil {
		logger.ErrorContext(ctx, "identity synchronization failed", "error", err)
	}
}

func runDirectoryReconciliation(ctx context.Context, accountService *accounts.Service, fileService *files.Service, logger *slog.Logger) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// File operations run as the user through the File Broker, which
			// needs a valid session; users without one are reconciled on their
			// next visit.
			for _, session := range accountService.RememberedSessions(ctx) {
				userCtx := accounts.WithSessionToken(ctx, session.Token)
				if err := fileService.ReconcileVisibleSpaces(userCtx, session.User); err != nil && !errors.Is(err, files.ErrVolumeUnavailable) {
					logger.ErrorContext(ctx, "directory reconciliation failed", "user_id", session.User.ID, "error", err)
				}
			}
		}
	}
}

type productOperations interface {
	accounts.CredentialProvisioner
	storage.VolumeExecutor
	files.SnapshotBackend
}

func configuredServices() (hoststate.Observer, httpapi.DataSource, productOperations, bool, error) {
	switch mode := os.Getenv("ANAS_HOSTSTATE_MODE"); mode {
	case "", "fake":
		operations := &developmentOperations{}
		return fake.NewHealthy(), httpapi.DataSourceSimulated, operations, false, nil
	case "agent":
		socketPath, err := agent.SocketPath()
		if err != nil {
			return nil, "", nil, false, err
		}
		client := agent.NewClient(socketPath)
		return client, httpapi.DataSourceLive, client, true, nil
	default:
		return nil, "", nil, false, errors.New("ANAS_HOSTSTATE_MODE must be fake or agent")
	}
}

// fileBrokerSocket defaults to the File Broker socket beside the Host Agent's.
func fileBrokerSocket() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("ANAS_FILE_BROKER_SOCKET")); configured != "" {
		if !filepath.IsAbs(configured) {
			return "", errors.New("ANAS_FILE_BROKER_SOCKET must be an absolute path")
		}
		return filepath.Clean(configured), nil
	}
	socketPath, err := agent.SocketPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(socketPath), "file-broker.sock"), nil
}

func stateDirectory() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("ANAS_STATE_DIR")); configured != "" {
		if !filepath.IsAbs(configured) {
			return "", errors.New("ANAS_STATE_DIR must be an absolute path")
		}
		return filepath.Clean(configured), nil
	}
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, "a-nas", "state"), nil
}

func environment(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

type developmentOperations struct{}

func (*developmentOperations) SetCredential(context.Context, accounts.CredentialRequest) error {
	return nil
}
func (*developmentOperations) DisableCredential(context.Context, string) error { return nil }

// Development volumes have no ACLs; viewing access is decided by the Policy.
func (*developmentOperations) GrantViewing(context.Context, accounts.ViewingRequest) error {
	return nil
}
func (*developmentOperations) RevokeViewing(context.Context, string) error { return nil }
func (*developmentOperations) CreateVolume(_ context.Context, request storage.CreateVolumeRequest) (storage.Volume, error) {
	return storage.Volume{ID: "volume:data", DiskID: request.DiskID, FilesystemUUID: "development", State: storage.VolumeStateAvailable}, nil
}
func (*developmentOperations) Create(context.Context, string, string) ([]files.SnapshotObject, error) {
	return nil, errors.New("development snapshot backend is not configured")
}
func (*developmentOperations) Open(context.Context, string, string) (io.ReadCloser, files.SnapshotObject, error) {
	return nil, files.SnapshotObject{}, files.ErrNotFound
}
func (*developmentOperations) Delete(context.Context, string, string) error { return files.ErrNotFound }

func configuredTerminal() (bool, error) {
	switch mode := os.Getenv("ANAS_TERMINAL"); mode {
	case "", "disabled":
		return false, nil
	case "enabled":
		return true, nil
	default:
		return false, errors.New("ANAS_TERMINAL must be enabled or disabled")
	}
}

// configuredContainers selects container management and the App Center
// together, since both run through the container agent. It defaults to the
// Fake Adapters only alongside simulated host state, so a live deployment
// never shows invented containers or apps.
func configuredContainers(hostSource httpapi.DataSource) (containers.Manager, appstore.Store, containersapi.DataSource, error) {
	mode := os.Getenv("ANAS_CONTAINERS_MODE")
	if mode == "" {
		mode = "disabled"
		if hostSource == httpapi.DataSourceSimulated {
			mode = "fake"
		}
	}
	switch mode {
	case "fake":
		apps, err := fakeappstore.New(700 * time.Millisecond)
		if err != nil {
			return nil, nil, "", err
		}
		return fakecontainers.New(), apps, containersapi.DataSourceSimulated, nil
	case "agent":
		socketPath, err := containeragent.SocketPath()
		if err != nil {
			return nil, nil, "", err
		}
		return containeragent.NewClient(socketPath), appstoreagent.NewClient(socketPath), containersapi.DataSourceLive, nil
	case "disabled":
		return nil, nil, containersapi.DataSourceLive, nil
	default:
		return nil, nil, "", errors.New("ANAS_CONTAINERS_MODE must be fake, agent or disabled")
	}
}
