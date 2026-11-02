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

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/envconfig"
	"github.com/zhongwater123/A-NAS/internal/family"
	"github.com/zhongwater123/A-NAS/internal/familyapi"
	"github.com/zhongwater123/A-NAS/internal/familyservice"
	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/sessionlookup"
)

const familyServiceCommand = "family-service"

func runFamilyService(logger *slog.Logger) error {
	if os.Geteuid() == 0 {
		return errors.New("the family service must run as its own identity, never root")
	}
	config, err := loadFamilyServiceConfig(envconfig.Environment())
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return familyservice.Run(ctx, familyservice.Config{
		Root:       config.Root,
		SocketPath: config.Socket,
		Sessions:   sessionlookup.NewClient(config.SessionSocket),
		Photos:     familyservice.PhotoAPI(config.PhotosSocket, logger),
		Media:      config.Media.client(),
		Logger:     logger,
	})
}

// configuredFamily returns the family API. A NAS forwards it to the family
// service; development serves it in this process from a directory on the
// development volume, asking the photo API it also serves here.
func configuredFamily(config Config, users *accounts.Service, photoAPI http.Handler, media ffmpegworker.Worker, logger *slog.Logger) (http.Handler, func(), error) {
	if config.Live() {
		return familyservice.Proxy(config.FamilySocket, logger), func() {}, nil
	}
	root := filepath.Join(config.DataMount, "family")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, nil, err
	}
	store, err := family.Open(root, family.Options{
		Accounts: familyservice.Accounts(func(ctx context.Context) ([]accounts.Profile, error) {
			active, err := users.ActiveUsers(ctx)
			if err != nil {
				return nil, err
			}
			profiles := make([]accounts.Profile, 0, len(active))
			for _, user := range active {
				profiles = append(profiles, accounts.Profile{ID: user.ID, Username: user.Username})
			}
			return profiles, nil
		}),
		Photos: familyservice.Photos{API: photoAPI},
	})
	if err != nil {
		return nil, nil, err
	}
	if err := store.Purge(context.Background()); err != nil {
		logger.Warn("family trash purge failed", "error", err)
	}
	return familyapi.New(store, logger, media), func() { _ = store.Close() }, nil
}
