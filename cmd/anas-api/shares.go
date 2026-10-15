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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return shareservice.Run(ctx, shareservice.Config{
		Root:         environment("ANAS_SHARES_ROOT", "/srv/a-nas/data/shares"),
		SocketPath:   environment("ANAS_SHARES_SOCKET", "/run/a-nas-shares/shares.sock"),
		BridgeSocket: environment("ANAS_SHARE_FILES_SOCKET", "/run/a-nas-share-files/files.sock"),
		Sessions:     sessionlookup.NewClient(environment("ANAS_SHARE_SESSION_SOCKET", "/run/a-nas-share-sessions/shares.sock")),
		Logger:       logger, Media: mediaworker.Client{Socket: environment("ANAS_MEDIA_SOCKET", "/run/a-nas-media/media.sock")},
	})
}

func configuredShares(ctx context.Context, live bool, volumeRoot string, users *accounts.Service, fs *files.Service, media mediaworker.Worker, logger *slog.Logger) (http.Handler, func(), error) {
	local := sharefiles.NewLocal(fs, users)
	if live {
		socket, err := fileBrokerSocket()
		if err != nil {
			return nil, nil, err
		}
		local.SetBroker(filebroker.NewClient(socket))
		closeBridge, err := sharefiles.Serve(ctx, environment("ANAS_SHARE_FILES_SOCKET", "/run/a-nas-share-files/files.sock"), local)
		if err != nil {
			return nil, nil, err
		}
		return sharesapi.NewProxy(environment("ANAS_SHARES_SOCKET", "/run/a-nas-shares/shares.sock"), logger), closeBridge, nil
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
	root := filepath.Join(volumeRoot, "shares")
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
