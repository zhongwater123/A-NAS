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
	"time"

	"github.com/zhongwater123/A-NAS/internal/accountremoval"
	"github.com/zhongwater123/A-NAS/internal/envconfig"
	"github.com/zhongwater123/A-NAS/internal/montage"
	"github.com/zhongwater123/A-NAS/internal/montageapi"
	"github.com/zhongwater123/A-NAS/internal/montagecompute"
	"github.com/zhongwater123/A-NAS/internal/montageservice"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
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
		JobCompute: montagecompute.Client{SocketPath: config.ComputeSocket},
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
func planner(answers string) montage.Planner {
	if answers == "" {
		return nil
	}
	return montage.FixedPlanner{Dir: answers}
}

// configuredMontage returns the montage API, nil while ANAS_MONTAGE_ENABLED
// is off: then /api/v1/montage is not mounted and the desktop shows no
// entry. A NAS forwards it to the montage service; development serves it in
// this process from a directory on the development volume, with the grants
// of the photo library in this process, and keeps the store as the service
// does (montage.Store.Upkeep). Deleted accounts leave the montage store
// whatever the switch: the service runs either way, and rows from a time it
// was on must not stay.
func configuredMontage(ctx context.Context, config Config, grants montage.Grants, media montage.Media, logger *slog.Logger) (http.Handler, func(context.Context, string) error, func(), error) {
	if config.Live() {
		remove := func(ctx context.Context, userID string) error {
			_, err := accountremoval.Request(ctx, config.MontageSocket, userID)
			return err
		}
		if !config.MontageEnabled {
			return nil, remove, func() {}, nil
		}
		return montageservice.Proxy(config.MontageSocket, logger), remove, func() {}, nil
	}
	root := filepath.Join(config.DataMount, "montage")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, nil, nil, err
	}
	// Development has no music analysis or compute Worker of its own unless
	// their sockets are given; songs wait for them.
	editing, _ := media.(montage.Editing)
	options := montage.Options{Grants: grants, Media: media, Editing: editing, Planner: planner(config.MontagePlanAnswers), Logger: logger}
	if config.MontageMusicSocket != "" {
		options.Music = montage.MusicWorker{Socket: config.MontageMusicSocket}
	}
	if config.MontageComputeSocket != "" {
		client := montagecompute.Client{SocketPath: config.MontageComputeSocket}
		options.Compute, options.JobCompute = client, client
	}
	store, err := montage.Open(ctx, root, options)
	if err != nil {
		return nil, nil, nil, err
	}
	upkeep, stopUpkeep := context.WithCancel(ctx)
	upkept := make(chan struct{})
	go func() {
		defer close(upkept)
		ticker := time.NewTicker(montage.UpkeepInterval)
		defer ticker.Stop()
		for {
			store.Upkeep(upkeep)
			select {
			case <-upkeep.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	closeStore := func() {
		stopUpkeep()
		<-upkept
		_ = store.Close()
	}
	if !config.MontageEnabled {
		return nil, store.RemoveAccount, closeStore, nil
	}
	if err := store.StartWorkers(ctx); err != nil {
		closeStore()
		return nil, nil, nil, err
	}
	// The headers the service gets are those the socket would carry.
	return serviceproxy.Local(montageapi.New(store, logger)), store.RemoveAccount, closeStore, nil
}
