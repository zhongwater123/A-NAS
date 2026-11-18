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
	"github.com/zhongwater123/A-NAS/internal/montage"
	"github.com/zhongwater123/A-NAS/internal/montageapi"
	"github.com/zhongwater123/A-NAS/internal/montagecompute"
	"github.com/zhongwater123/A-NAS/internal/montageservice"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/sessionlookup"
)

const montageServiceCommand = "montage-service"

func runMontageService(logger *slog.Logger) error {
	if os.Geteuid() == 0 {
		return errors.New("the montage service must run as its own identity, never root")
	}
	config, err := loadMontageServiceConfig(envconfig.Environment())
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return montageservice.Run(ctx, montageservice.Config{
		Root:       config.Root,
		SocketPath: config.Socket,
		Sessions:   sessionlookup.NewClient(config.SessionSocket),
		Grants:     photosapi.GrantClient(config.GrantSocket),
		Media:      config.Media.client(),
		Music:      montage.MusicWorker{Socket: config.MusicSocket},
		Compute:    montagecompute.Client{SocketPath: config.ComputeSocket},
		Logger:     logger,
	})
}

// configuredMontage returns the montage API, or nil while
// ANAS_MONTAGE_ENABLED is off: then /api/v1/montage is not mounted and the
// desktop shows no entry. A NAS forwards it to the montage service;
// development serves it in this process from a directory on the development
// volume, with grants the photo library in this process holds.
func configuredMontage(ctx context.Context, config Config, grants montage.Grants, media montage.Media, logger *slog.Logger) (http.Handler, func(context.Context, string) error, func(), error) {
	forget := func(context.Context, string) error { return nil }
	if !config.MontageEnabled {
		return nil, forget, func() {}, nil
	}
	if config.Live() {
		remove := func(ctx context.Context, userID string) error {
			_, err := accountremoval.Request(ctx, config.MontageSocket, userID)
			return err
		}
		return montageservice.Proxy(config.MontageSocket, logger), remove, func() {}, nil
	}
	root := filepath.Join(config.DataMount, "montage")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, nil, nil, err
	}
	// Development has no music analysis or compute Worker of its own unless
	// their sockets are given; songs wait for them.
	options := montage.Options{Grants: grants, Media: media, Logger: logger}
	if config.MontageMusicSocket != "" {
		options.Music = montage.MusicWorker{Socket: config.MontageMusicSocket}
	}
	if config.MontageComputeSocket != "" {
		options.Compute = montagecompute.Client{SocketPath: config.MontageComputeSocket}
	}
	store, err := montage.Open(ctx, root, options)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := store.Reconcile(ctx); err != nil {
		logger.Warn("reconciling montage projects with the photo library failed", "error", err)
	}
	if err := store.ReclaimSongs(ctx); err != nil {
		logger.Warn("reclaiming montage songs failed", "error", err)
	}
	analysis, stop := context.WithCancel(ctx)
	analysed := make(chan struct{})
	go func() { defer close(analysed); store.AnalyzeSongs(analysis) }()
	return montageapi.New(store, logger), store.RemoveAccount, func() { stop(); <-analysed; _ = store.Close() }, nil
}
