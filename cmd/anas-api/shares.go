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
	"github.com/zhongwater123/A-NAS/internal/filebroker"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/mediaworker"
	"github.com/zhongwater123/A-NAS/internal/sessionlookup"
	"github.com/zhongwater123/A-NAS/internal/sharefiles"
	"github.com/zhongwater123/A-NAS/internal/shares"
	"github.com/zhongwater123/A-NAS/internal/sharesapi"
	"github.com/zhongwater123/A-NAS/internal/shareservice"
)

func runShareService(logger *slog.Logger) error {
	if os.Geteuid() == 0 {
		return errors.New("the share service must run as its own identity, never root")
	}
	config, err := loadShareServiceConfig(envconfig.Environment())
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return shareservice.Run(ctx, shareservice.Config{
		Root:         config.Root,
		SocketPath:   config.Socket,
		BridgeSocket: config.FilesSocket,
		Sessions:     sessionlookup.NewClient(config.SessionSocket),
		Logger:       logger, Media: config.Media.client(),
	})
}

func configuredShares(ctx context.Context, config Config, users *accounts.Service, fs *files.Service, media mediaworker.Worker, logger *slog.Logger) (http.Handler, func(), error) {
	local := sharefiles.NewLocal(fs, users)
	if config.Live() {
		local.SetBroker(filebroker.NewClient(config.FileBrokerSocket))
		closeBridge, err := sharefiles.Serve(ctx, config.ShareFilesSocket, local)
		if err != nil {
			return nil, nil, err
		}
		return shareservice.Proxy(config.SharesSocket, logger), closeBridge, nil
	}
	members := func(ctx context.Context) ([]accounts.Profile, error) {
		active, err := users.ActiveUsers(ctx)
		if err != nil {
			return nil, err
		}
		out := []accounts.Profile{}
		for _, u := range active {
			out = append(out, accounts.Profile{ID: u.ID, Username: u.Username, Nickname: u.Nickname, AvatarVersion: u.AvatarVersion})
		}
		return out, nil
	}
	root := filepath.Join(config.DataMount, "shares")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, nil, err
	}
	store, err := shares.Open(filepath.Join(root, "shares.db"), filepath.Join(root, "content"), shares.Options{
		Backend: sharefiles.NewBackend(local),
		Active: func(ctx context.Context, id string) (bool, error) {
			list, err := members(ctx)
			if err != nil {
				return false, err
			}
			for _, p := range list {
				if p.ID == id {
					return true, nil
				}
			}
			return false, nil
		},
		Authorize: func(ctx context.Context, user shares.User) error {
			session, err := users.ResolveSession(ctx, accounts.SessionToken(ctx))
			if err != nil && !errors.Is(err, accounts.ErrSessionNotFound) {
				return err
			}
			if err != nil || session.User.ID != user.ID || session.User.MustChangePassword {
				return shares.ErrUnauthorized
			}
			return nil
		},
	})
	if err != nil {
		return nil, nil, err
	}
	return sharesapi.NewWithPreview(store, logger, members, media), func() { _ = store.Interrupt("Share service stopped before task completion") }, nil
}
