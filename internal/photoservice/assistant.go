package photoservice

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"

	"golang.org/x/sys/unix"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
)

// A member's AI assistant reads the photo library through a socket of its own
// (ADR 0024). It has no session: the assistant runs as the member's Linux
// UID, so the kernel's peer credentials say whom it acts for and the Host
// Agent confirms that account is active. The Product Service is not in the
// path and cannot claim to be anyone's assistant (ADR 0011 decision 4).

// AccountResolver names the active user who owns a Linux UID.
type AccountResolver interface {
	ResolveAccountUID(context.Context, int) (accounts.SessionUser, error)
}

// assistantRoutes are the reads the assistant's photo tools use. Originals,
// thumbnails, the AI status, the activity log and uploads stay out of reach,
// and nothing changes; a photo's pixels leave only as its view (a 1600-pixel
// JPEG without the original's metadata), for the model to look at when the
// member asks it to, and its metadata, location included, as read from the
// original.
var assistantRoutes = []string{
	"/libraries",
	"/libraries/{libraryID}/timeline",
	"/libraries/{libraryID}/timeline/months",
	"/libraries/{libraryID}/albums",
	"/albums/{albumID}/assets",
	"/search",
	"/assets/{assetID}",
	"/assets/{assetID}/similar",
	"/assets/{assetID}/view",
	"/assets/{assetID}/metadata",
	"/tags",
	"/tags/{name}/assets",
}

type peerUIDKey struct{}

// withPeerUID records on each connection the UID of the process at the other
// end, as the kernel reports it.
func withPeerUID(ctx context.Context, conn net.Conn) context.Context {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return ctx
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return ctx
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil || credErr != nil {
		return ctx
	}
	return context.WithValue(ctx, peerUIDKey{}, int(cred.Uid))
}

// assistantAPI serves only assistantRoutes, and only for reading; anything
// else is not_found, in the photo API's error format.
func assistantAPI(api http.Handler) http.Handler {
	mux := http.NewServeMux()
	for _, route := range assistantRoutes {
		mux.Handle("GET "+photosapi.PathPrefix+route, api)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "the assistant cannot use this part of the photo library")
	})
	return mux
}

// authenticateAssistant turns the socket peer's UID into the Principal of
// that member's assistant. Its requests keep background AI work waiting, as
// its member's own do.
func authenticateAssistant(resolver AccountResolver, logger *slog.Logger, gate *Gate, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, ok := r.Context().Value(peerUIDKey{}).(int)
		if !ok || uid <= 0 {
			// Only assistants, running as members, reach this socket: no credentials
			// or root at the other end means it is wired wrong.
			logger.ErrorContext(r.Context(), "assistant socket peer is not a member", "uid", uid, "credentials", ok)
			httpjson.WriteError(w, http.StatusServiceUnavailable, "photos_unavailable", "the photo library is not available on this device")
			return
		}
		user, err := resolver.ResolveAccountUID(r.Context(), uid)
		switch {
		case errors.Is(err, accounts.ErrUserNotFound):
			httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
			return
		case err != nil:
			logger.ErrorContext(r.Context(), "account lookup failed", "error", err)
			httpjson.WriteError(w, http.StatusServiceUnavailable, "photos_unavailable", "the photo library is not available on this device")
			return
		case user.MustChangePassword:
			httpjson.WriteError(w, http.StatusForbidden, "password_change_required", "choose a new password before continuing")
			return
		}
		gate.Touch()
		principal := photosapi.NewAssistantPrincipal(user.UserID, user.Username)
		next.ServeHTTP(w, r.WithContext(photosapi.WithPrincipal(r.Context(), principal)))
	})
}
