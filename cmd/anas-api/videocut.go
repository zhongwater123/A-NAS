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
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
	"github.com/zhongwater123/A-NAS/internal/sessionlookup"
	"github.com/zhongwater123/A-NAS/internal/storeservice"
	"github.com/zhongwater123/A-NAS/internal/videocut"
	"github.com/zhongwater123/A-NAS/internal/videocut/app"
	"github.com/zhongwater123/A-NAS/internal/videocut/external"
	"github.com/zhongwater123/A-NAS/internal/videocut/protocol"
	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

const videoCutServiceCommand = "montage-service"

func runVideoCutService(logger *slog.Logger) error {
	if os.Geteuid() == 0 {
		return errors.New("the montage service must run as its own identity, never root")
	}
	config, err := loadVideoCutServiceConfig(envconfig.Environment())
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return videocut.Run(ctx, videocut.Config{
		Root:       config.Root,
		SocketPath: config.Socket,
		Sessions:   sessionlookup.NewClient(config.SessionSocket),
		Grants:     photosapi.GrantClient(config.GrantSocket),
		Media:      config.Media.client(),
		Music:      external.MusicWorker{Socket: config.MusicSocket},
		Compute:    videocutcompute.Client{SocketPath: config.ComputeSocket},
		JobCompute: videocutcompute.Client{SocketPath: config.ComputeSocket},
		Editing:    config.Media.client(),
		Planner:    planner(config.PlanAnswers),
		// The member's VNAS plans on the montage center's assistant socket.
		Accounts:        sessionlookup.NewClient(config.SessionSocket),
		AssistantSocket: config.AssistantSocket,
		PlanMarkers:     config.PlanMarkers,
		Logger:          logger,
	})
}

// planner is the stand-in planner when PlanAnswers names one.
func planner(answers string) app.Planner {
	if answers == "" {
		return nil
	}
	return external.FixedPlanner{Dir: answers}
}

// configuredVideoCut returns the montage API, nil while ANAS_VIDEOCUT_ENABLED
// is off: then /api/v1/videocut is not mounted and the desktop shows no
// entry. A NAS forwards it to the montage service; development serves it in
// this process from a directory on the development volume, with the grants
// of the photo library in this process, and keeps the store as the service
// does (app.Store.Upkeep). Deleted accounts leave the montage store
// whatever the switch: the service runs either way, and rows from a time it
// was on must not stay.
func configuredVideoCut(ctx context.Context, config Config, grants app.Grants, media app.Media, logger *slog.Logger) (http.Handler, func(context.Context, string) error, func(), error) {
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
	root := filepath.Join(config.DataMount, "montage")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, nil, nil, err
	}
	// Development has no music analysis or compute Worker of its own unless
	// their sockets are given; songs wait for them.
	editing, _ := media.(app.Editing)
	options := app.Options{Grants: grants, Media: media, Editing: editing, Planner: planner(config.VideoCutPlanAnswers),
		WindowSearch: videocut.WindowSearchOf(grants), Logger: logger}
	if config.VideoCutMusicSocket != "" {
		options.Music = external.MusicWorker{Socket: config.VideoCutMusicSocket}
	}
	if config.VideoCutComputeSocket != "" {
		client := videocutcompute.Client{SocketPath: config.VideoCutComputeSocket}
		options.Compute, options.JobCompute = client, client
	}
	store, err := app.Open(ctx, root, options)
	if err != nil {
		return nil, nil, nil, err
	}
	stopUpkeep := storeservice.Every(ctx, app.UpkeepInterval, store.Upkeep)
	closeStore := func() {
		stopUpkeep()
		_ = store.Close()
	}
	if !config.VideoCutEnabled {
		return nil, store.RemoveAccount, closeStore, nil
	}
	if err := store.StartWorkers(ctx); err != nil {
		closeStore()
		return nil, nil, nil, err
	}
	// The headers the service gets are those the socket would carry.
	return serviceproxy.Local(protocol.New(store, logger)), store.RemoveAccount, closeStore, nil
}
