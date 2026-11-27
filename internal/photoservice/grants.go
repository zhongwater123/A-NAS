package photoservice

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/unixmsg"
)

// The montage center's project grants (ADR 0038 decision 3) are served on a
// socket systemd holds for the montage account alone
// (anas-photos-grants.socket, mode 0600); the photo service checks the
// peer's UID as well. A request there is not someone using the photo
// library: it does not hold background AI work back, except a search of
// windows, which a member editing a film waits for.

// ActiveAccounts lists the accounts in use, as the Host Agent's session
// lookup does.
type ActiveAccounts interface {
	ActiveProfiles(context.Context) ([]accounts.Profile, error)
}

// grantDeadline bounds one request on the grant socket, its Host Agent calls
// included.
const grantDeadline = 2 * time.Minute

// grantServer answers the grant socket with the store open now.
type grantServer struct {
	listener net.Listener
	service  *atomic.Pointer[photos.Service]
	sessions SessionResolver
	accounts ActiveAccounts
	// gate, when set, learns of the searches a member waits for.
	gate    *Gate
	peerUID int
	logger  *slog.Logger
	// base ends the Host Agent calls of the requests still open when
	// stopping gives up waiting for them.
	base   context.Context
	cancel context.CancelFunc
	// answers are the requests being answered; serve adds to it until
	// stopped closes.
	answers sync.WaitGroup
	stopped chan struct{}
	closing sync.Once
}

func newGrantServer(listener net.Listener, service *atomic.Pointer[photos.Service], sessions SessionResolver,
	active ActiveAccounts, gate *Gate, peerUID int, logger *slog.Logger) *grantServer {
	base, cancel := context.WithCancel(context.Background())
	return &grantServer{listener: listener, service: service, sessions: sessions, accounts: active, gate: gate, peerUID: peerUID,
		logger: logger, base: base, cancel: cancel, stopped: make(chan struct{})}
}

// grantSocket is the listener systemd hands over by the name "grants", or,
// in development and tests, one on socketPath.
func grantSocket(socketPath string) (net.Listener, bool, error) {
	if listeners, err := systemdListeners(); err == nil && len(listeners["grants"]) > 0 {
		return listeners["grants"][0], true, nil
	}
	if socketPath == "" {
		return nil, false, nil
	}
	listener, err := listen(socketPath, 0o600)
	return listener, false, err
}

// serve accepts until the listener closes. As in net/http, a failed accept,
// such as while the process is out of descriptors, is waited out: it never
// ends the photo service, which every member uses.
func (g *grantServer) serve() error {
	defer close(g.stopped)
	var delay time.Duration
	for {
		conn, err := g.listener.Accept()
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		if err != nil {
			delay = min(max(2*delay, 5*time.Millisecond), time.Second)
			g.logger.Warn("grant socket accept failed; retrying", "error", err, "retry_in", delay)
			time.Sleep(delay)
			continue
		}
		delay = 0
		g.answers.Go(func() { g.answer(conn) })
	}
}

// drain stops accepting and waits, until ctx ends, for the requests being
// answered, so that they finish before the Catalog closes. Requests still
// open then lose their Host Agent calls.
func (g *grantServer) drain(ctx context.Context) {
	g.closing.Do(func() { _ = g.listener.Close() })
	done := make(chan struct{})
	go func() {
		<-g.stopped
		g.answers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		g.cancel()
		g.logger.Warn("grant requests were still open when the photo service stopped")
	}
}

func refused(code, message string) photosapi.GrantResponse {
	return photosapi.GrantResponse{Code: code, Message: message}
}

func (g *grantServer) answer(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	if uid, ok := peerUID(conn); !ok || uid != g.peerUID {
		g.logger.Error("grant socket peer is not the montage service", "uid", uid, "credentials", ok)
		return
	}
	unixConn := conn.(*net.UnixConn)
	deadline := time.Now().Add(grantDeadline)
	_ = conn.SetDeadline(deadline)
	var request photosapi.GrantRequest
	file, err := unixmsg.Read(unixConn, &request, photosapi.GrantMessageLimit)
	defer unixmsg.CloseFile(file)
	if err != nil {
		return
	}
	if request.Op == photosapi.GrantSaveFilm {
		deadline = time.Now().Add(photosapi.SaveFilmTimeout)
		_ = conn.SetDeadline(deadline)
	}
	ctx, cancel := context.WithDeadline(g.base, deadline)
	defer cancel()
	response, opened := g.handleWith(ctx, request, file)
	if opened != nil {
		defer func() { _ = opened.Close() }()
	}
	if err := unixmsg.Write(unixConn, response, opened, photosapi.GrantMessageLimit); err != nil {
		g.logger.Warn("grant answer not sent", "op", request.Op, "error", err)
	}
}

// owner is the grant's owner as a grant Principal, while the account is in
// use.
func (g *grantServer) owner(ctx context.Context, service *photos.Service, grantID string) (photos.Principal, error) {
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

// handleWith answers request; given is the descriptor that came with it.
func (g *grantServer) handleWith(ctx context.Context, request photosapi.GrantRequest, given *os.File) (photosapi.GrantResponse, *os.File) {
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
	case photosapi.GrantLookup, photosapi.GrantOpen, photosapi.GrantRemove, photosapi.GrantAnalyze, photosapi.GrantAnalysis, photosapi.GrantSaveFilm,
		photosapi.GrantSearchWindows:
		if request.Op == photosapi.GrantSearchWindows && g.gate != nil {
			// As a photo search: someone is using the library until it is answered.
			defer g.gate.Use()()
		}
		var owner photos.Principal
		if owner, err = g.owner(ctx, service, request.GrantID); err != nil {
			break
		}
		switch request.Op {
		case photosapi.GrantLookup:
			response.Assets, err = service.LookupGrant(ctx, owner, request.GrantID, request.AssetIDs)
		case photosapi.GrantOpen:
			opened, response.ObjectID, err = service.OpenGrant(ctx, owner, request.GrantID, request.AssetID)
		case photosapi.GrantSaveFilm:
			if given == nil {
				return refused(photosapi.GrantInvalid, "saveFilm takes the film's descriptor"), nil
			}
			response.ObjectID, err = service.SaveFilm(ctx, owner, request.GrantID, request.VersionKey, request.Name, given)
		case photosapi.GrantAnalyze:
			err = service.AnalyzeGrant(ctx, owner, request.GrantID, request.AssetIDs, request.Retry)
		case photosapi.GrantSearchWindows:
			response.Matches, err = service.SearchWindows(ctx, owner, request.GrantID, request.Texts, request.K)
		case photosapi.GrantAnalysis:
			var analysis photos.GrantAnalysis
			if analysis, opened, err = service.AnalysisOfGrant(ctx, owner, request.GrantID, request.AssetID); err == nil {
				response.Analysis = &analysis
			}
		default:
			err = service.RemoveFromGrant(ctx, owner, request.GrantID, request.AssetIDs)
		}
	case photosapi.GrantList:
		response.Grants, err = service.Grants(ctx, request.After, photosapi.GrantListPage)
		if err == nil && len(response.Grants) == photosapi.GrantListPage {
			response.Next = response.Grants[len(response.Grants)-1].ID
		}
	case photosapi.GrantRevoke:
		err = service.RevokeGrant(ctx, request.GrantID)
	default:
		return refused(photosapi.GrantInvalid, "unknown operation"), nil
	}
	switch {
	case err == nil:
		response.OK = true
		return response, opened
	case errors.Is(err, photos.ErrGrantNotFound):
		return refused(photosapi.GrantNotFound, "no such grant"), nil
	case errors.Is(err, errInactiveOwner):
		return refused(photosapi.GrantOwnerInactive, "the grant's owner is not an active account"), nil
	case errors.Is(err, photos.ErrGrantAssets), errors.Is(err, photos.ErrForbidden):
		return refused(photosapi.GrantAssetsRefused, "an asset cannot be granted"), nil
	case errors.Is(err, photos.ErrTooManyIDs):
		return refused(photosapi.GrantTooMany, "too many assets"), nil
	case errors.Is(err, photos.ErrNotFound):
		return refused(photosapi.GrantAssetNotFound, "the asset is not available"), nil
	case errors.Is(err, photos.ErrInsufficientSpace):
		return refused(photosapi.GrantNoSpace, "not enough space for the film"), nil
	case errors.Is(err, photos.ErrInvalidName), errors.Is(err, photos.ErrUnsupportedType), errors.Is(err, photos.ErrInvalidQuery):
		return refused(photosapi.GrantInvalid, err.Error()), nil
	case errors.Is(err, photos.ErrWindowModelMismatch):
		return refused(photosapi.GrantModelMismatch, "the windows were made with another model; analyse the videos again"), nil
	case errors.Is(err, photos.ErrAIUnavailable):
		g.logger.Warn("grant search could not encode its texts", "error", err)
		return refused(photosapi.GrantAIUnavailable, "local AI is not available"), nil
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
// with the same checks as the grant socket. Its searches hold back the
// background AI work gate lets through, as the grant socket's do; a nil
// gate holds nothing back.
func LocalGrants(service *photos.Service, sessions SessionsAndAccounts, gate *Gate, logger *slog.Logger) photosapi.Grants {
	var opened atomic.Pointer[photos.Service]
	opened.Store(service)
	server := &grantServer{service: &opened, sessions: sessions, accounts: sessions, gate: gate, logger: logger}
	send := func(ctx context.Context, request photosapi.GrantRequest, given *os.File) (photosapi.GrantResponse, *os.File, error) {
		response, file := server.handleWith(ctx, request, given)
		if !response.OK {
			return photosapi.GrantResponse{}, nil, &photosapi.GrantError{Code: response.Code, Message: response.Message}
		}
		return response, file, nil
	}
	return photosapi.Grants{
		Call: func(ctx context.Context, request photosapi.GrantRequest) (photosapi.GrantResponse, *os.File, error) {
			return send(ctx, request, nil)
		},
		Send: send,
	}
}
