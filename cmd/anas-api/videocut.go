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
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/sessionlookup"
	"github.com/zhongwater123/A-NAS/internal/videocut"
)

const videoCutServiceCommand = "videocut-service"

func runVideoCutService(logger *slog.Logger) error {
	if os.Geteuid() == 0 {
		return errors.New("the VideoCut service must run as its own identity, never root")
	}
	config, err := loadVideoCutServiceConfig(envconfig.Environment())
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return videocut.Run(ctx, videocut.Config{
		Root:          config.Root,
		SocketPath:    config.Socket,
		Sessions:      sessionlookup.NewClient(config.SessionSocket),
		Grants:        photosapi.GrantClient(config.GrantSocket),
		Media:         config.Media.client(),
		MusicSocket:   config.MusicSocket,
		ComputeSocket: config.ComputeSocket,
		PlanAnswers:   config.PlanAnswers,
		// The member's VNAS plans on VideoCut's assistant socket.
		Accounts:        sessionlookup.NewClient(config.SessionSocket),
		AssistantSocket: config.AssistantSocket,
		PlanMarkers:     config.PlanMarkers,
		Logger:          logger,
	})
}

// configuredVideoCut returns the VideoCut API, nil while ANAS_VIDEOCUT_ENABLED
// is off: then /api/v1/videocut is not mounted and the desktop shows no
// entry. A NAS forwards it to the VideoCut service; development serves it in
// this process from a directory on the development volume, with the grants
// of the photo library in this process (videocut.Local). Deleted accounts
// leave the VideoCut store whatever the switch: the service runs either
// way, and rows from a time it was on must not stay.
func configuredVideoCut(ctx context.Context, config Config, grants videocut.Grants, media videocut.Media, logger *slog.Logger) (http.Handler, func(context.Context, string) error, func(), error) {
	if config.Live() {
		remove := func(ctx context.Context, userID string) error {
			_, err := accountremoval.Request(ctx, config.VideoCutSocket, userID)
			return err
		}
		if !config.VideoCutEnabled {
			return nil, remove, func() {}, nil
		}
		return videocut.Proxy(config.VideoCutSocket, logger), remove, func() {}, nil
	}
	root := filepath.Join(config.DataMount, "videocut")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, nil, nil, err
	}
	// Development has no music analysis or compute Worker of its own unless
	// their sockets are given; songs wait for them.
	return videocut.Local(ctx, root, videocut.LocalConfig{Grants: grants, Media: media, MusicSocket: config.VideoCutMusicSocket,
		ComputeSocket: config.VideoCutComputeSocket, PlanAnswers: config.VideoCutPlanAnswers, Serve: config.VideoCutEnabled, Logger: logger})
}
