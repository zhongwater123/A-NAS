package photoservice

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"sync/atomic"
	"time"

	"github.com/coreos/go-systemd/v22/activation"
	"golang.org/x/sys/unix"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/unixmsg"
)

// The montage center's project grants (ADR 0038 decision 3) are served on a
// socket systemd holds for the montage account alone
// (anas-photos-grants.socket, mode 0600); the photo service checks the
// peer's UID as well. A request there is not someone using the photo
// library: it does not hold background AI work back.

// ActiveAccounts lists the accounts in use, as the Host Agent's session
// lookup does.
type ActiveAccounts interface {
	ActiveProfiles(context.Context) ([]accounts.Profile, error)
}

// grantServer answers the grant socket with the store open now.
type grantServer struct {
	service  *atomic.Pointer[photos.Service]
	sessions SessionResolver
	accounts ActiveAccounts
	peerUID  int
	logger   *slog.Logger
}

// grantSocket is the listener systemd hands over by the name "grants", or,
// in development and tests, one on socketPath.
func grantSocket(socketPath string) (net.Listener, bool, error) {
	if listeners, err := activation.ListenersWithNames(); err == nil && len(listeners["grants"]) > 0 {
		return listeners["grants"][0], true, nil
	}
	if socketPath == "" {
		return nil, false, nil
	}
	listener, err := listen(socketPath, 0o600)
	return listener, false, err
}

func (g grantServer) serve(listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go g.answer(conn.(*net.UnixConn))
	}
}

func peerUID(conn *net.UnixConn) (int, bool) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, false
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil || credErr != nil {
		return 0, false
	}
	return int(cred.Uid), true
}

func refused(code, message string) photosapi.GrantResponse {
	return photosapi.GrantResponse{Code: code, Message: message}
}

func (g grantServer) answer(conn *net.UnixConn) {
	defer func() { _ = conn.Close() }()
	if uid, ok := peerUID(conn); !ok || uid != g.peerUID {
		g.logger.Error("grant socket peer is not the montage service", "uid", uid, "credentials", ok)
		return
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
	var request photosapi.GrantRequest
	file, err := unixmsg.Read(conn, &request, photosapi.GrantMessageLimit)
	unixmsg.CloseFile(file)
	if err != nil {
		return
	}
	response, opened := g.handle(context.Background(), request)
	if opened != nil {
		defer func() { _ = opened.Close() }()
	}
	if err := unixmsg.Write(conn, response, opened, photosapi.GrantMessageLimit); err != nil {
		g.logger.Warn("grant answer not sent", "op", request.Op, "error", err)
	}
}

// owner is the grant's owner as a grant Principal, while the account is in
// use.
func (g grantServer) owner(ctx context.Context, service *photos.Service, grantID string) (photos.Principal, error) {
	grant, err := service.GrantOf(ctx, grantID)
	if err != nil {
		return photos.Principal{}, err
	}
	profiles, err := g.accounts.ActiveProfiles(ctx)
	if err != nil {
		return photos.Principal{}, err
	}
	for _, profile := range profiles {
		if profile.ID == grant.OwnerUserID {
			return photosapi.NewGrantPrincipal(profile.ID, profile.Username), nil
		}
	}
	return photos.Principal{}, errInactiveOwner
}

var errInactiveOwner = errors.New("the grant's owner is not an active account")

func (g grantServer) handle(ctx context.Context, request photosapi.GrantRequest) (photosapi.GrantResponse, *os.File) {
	service := g.service.Load()
	if service == nil {
		return refused(photosapi.GrantUnavailable, "the photo library is not available"), nil
	}
	var response photosapi.GrantResponse
	var opened *os.File
	var err error
	switch request.Op {
	case photosapi.GrantRegister:
		var user accounts.SessionUser
		user, err = g.sessions.ResolveSessionUser(ctx, request.Session)
		switch {
		case errors.Is(err, accounts.ErrSessionNotFound) || err == nil && (user.UserID == "" || user.MustChangePassword):
			return refused(photosapi.GrantUnauthorized, "the member's session is not valid"), nil
		case err != nil:
			break
		default:
			var grant photos.Grant
			grant, err = service.RegisterGrant(ctx, photosapi.NewGrantPrincipal(user.UserID, user.Username), request.ProjectID, request.AssetIDs)
			response.Grant = &grant
		}
	case photosapi.GrantLookup, photosapi.GrantOpen, photosapi.GrantRemove:
		var owner photos.Principal
		if owner, err = g.owner(ctx, service, request.GrantID); err != nil {
			break
		}
		switch request.Op {
		case photosapi.GrantLookup:
			response.Assets, err = service.LookupGrant(ctx, owner, request.GrantID, request.AssetIDs)
		case photosapi.GrantOpen:
			opened, response.ObjectID, err = service.OpenGrant(ctx, owner, request.GrantID, request.AssetID)
		default:
			err = service.RemoveFromGrant(ctx, owner, request.GrantID, request.AssetIDs)
		}
	case photosapi.GrantList:
		response.Grants, err = service.Grants(ctx)
	case photosapi.GrantRevoke:
		err = service.RevokeGrant(ctx, request.GrantID)
	default:
		return refused(photosapi.GrantInvalid, "unknown operation"), nil
	}
	switch {
	case err == nil:
		response.OK = true
		return response, opened
	case errors.Is(err, photos.ErrGrantNotFound), errors.Is(err, errInactiveOwner):
		return refused(photosapi.GrantNotFound, "no such grant"), nil
	case errors.Is(err, photos.ErrGrantAssets), errors.Is(err, photos.ErrForbidden):
		return refused(photosapi.GrantAssetsRefused, "an asset cannot be granted"), nil
	case errors.Is(err, photos.ErrTooManyIDs):
		return refused(photosapi.GrantTooMany, "too many assets"), nil
	case errors.Is(err, photos.ErrNotFound):
		return refused(photosapi.GrantAssetNotFound, "the asset is not available"), nil
	}
	g.logger.Error("grant request failed", "op", request.Op, "error", err)
	return refused(photosapi.GrantUnavailable, "the photo library could not answer"), nil
}

// SessionsAndAccounts confirms sessions and lists the accounts in use, as
// the Host Agent's session lookup does and development's account store.
type SessionsAndAccounts interface {
	SessionResolver
	ActiveAccounts
}

// LocalGrants serves the grant operations in this process, for development,
// with the same checks as the grant socket.
func LocalGrants(service *photos.Service, sessions SessionsAndAccounts, logger *slog.Logger) photosapi.Grants {
	var opened atomic.Pointer[photos.Service]
	opened.Store(service)
	server := grantServer{service: &opened, sessions: sessions, accounts: sessions, logger: logger}
	return photosapi.Grants{Call: func(ctx context.Context, request photosapi.GrantRequest) (photosapi.GrantResponse, *os.File, error) {
		response, file := server.handle(ctx, request)
		if !response.OK {
			return photosapi.GrantResponse{}, nil, &photosapi.GrantError{Code: response.Code, Message: response.Message}
		}
		return response, file, nil
	}}
}
