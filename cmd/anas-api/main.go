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
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/hoststate/agent"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/storage"
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
	if live {
		fileOptions.VolumeGuard = files.BtrfsVolumeGuard{ExpectedFilesystemUUID: func(ctx context.Context) (string, error) {
			volumes, err := storageService.ListVolumes(ctx)
			if err != nil || len(volumes) != 1 || volumes[0].State != storage.VolumeStateAvailable {
				return "", err
			}
			return volumes[0].FilesystemUUID, nil
		}}
		fileOptions.SnapshotBackend = operations
	} else if err := os.MkdirAll(volumeRoot, 0o700); err != nil {
		return err
	}
	fileService := files.NewService(fileStore, volumeRoot, accountService, fileOptions)

	apiHandler := httpapi.NewProduct(httpapi.ProductDependencies{
		Reader: reader, DataSource: dataSource, ProductVersion: version,
		Accounts: accountService, Files: fileService, Storage: storageService,
		Logger: logger,
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
	go runDirectoryReconciliation(ctx, accountService, fileService, logger)
	listenError := make(chan error, 1)
	go func() {
		logger.Info("A-NAS API listening", "address", address, "version", version, "data_source", dataSource)
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
		err := <-listenError
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
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
			users, err := accountService.ActiveUsers(ctx)
			if err != nil {
				logger.ErrorContext(ctx, "list users for directory reconciliation failed", "error", err)
				continue
			}
			for _, user := range users {
				if err := fileService.ReconcileVisibleSpaces(ctx, user); err != nil && !errors.Is(err, files.ErrVolumeUnavailable) {
					logger.ErrorContext(ctx, "directory reconciliation failed", "user_id", user.ID, "error", err)
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

func configuredServices() (hoststate.Reader, httpapi.DataSource, productOperations, bool, error) {
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
