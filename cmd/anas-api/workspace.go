package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/zhongwater123/A-NAS/internal/accountremoval"
	"github.com/zhongwater123/A-NAS/internal/envconfig"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
	"github.com/zhongwater123/A-NAS/internal/sessiondir"
	"github.com/zhongwater123/A-NAS/internal/sessionlookup"
	"github.com/zhongwater123/A-NAS/internal/storeservice"
	"github.com/zhongwater123/A-NAS/internal/workspace"
	"github.com/zhongwater123/A-NAS/internal/workspaceapi"
	"github.com/zhongwater123/A-NAS/internal/workspaceservice"
)

const workspaceServiceCommand = "workspace-service"

func runWorkspaceService(logger *slog.Logger) error {
	if os.Geteuid() == 0 {
		return errors.New("the workspace service must run as its own identity, never root")
	}
	config, err := loadWorkspaceServiceConfig(envconfig.Environment())
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return workspaceservice.Run(ctx, workspaceservice.Config{
		Root:       config.Root,
		SocketPath: config.Socket,
		Directory:  sessionlookup.NewClient(config.SessionSocket),
		Logger:     logger,
	})
}

// configuredWorkspace returns the shared workspace (ADR 0040). A NAS forwards
// it to the workspace service. Development serves it in this process from a
// directory on the development volume, with the same authentication the
// service does, reading control.db read-only as the Host Agent would.
func configuredWorkspace(config Config, controlDB string, logger *slog.Logger) (http.Handler, func(context.Context, string) error, func(), error) {
	if config.Live() {
		remove := func(ctx context.Context, userID string) error {
			_, err := accountremoval.Request(ctx, config.WorkspaceSocket, userID)
			return err
		}
		return workspaceservice.Proxy(config.WorkspaceSocket, logger), remove, func() {}, nil
	}
	root := filepath.Join(config.DataMount, "workspace")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, nil, nil, err
	}
	store, err := workspace.Open(root, workspace.Options{})
	if err != nil {
		return nil, nil, nil, err
	}
	directory := sessiondir.New(controlDB)
	stopUpkeep := storeservice.Every(context.Background(), workspaceservice.UpkeepInterval, func(ctx context.Context) {
		workspaceservice.Upkeep(ctx, store, logger)
	})
	closeStore := func() {
		stopUpkeep()
		_ = store.Close()
		_ = directory.Close()
	}
	api := workspaceapi.New(store, workspaceapi.Options{Logger: logger, Directory: directory})
	// The headers the service gets are those the socket would carry.
	return serviceproxy.Local(workspaceservice.Authenticate(directory, logger, api)), store.RemoveAccount, closeStore, nil
}
