package photoservice

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

type resolverFunc func(context.Context, int) (accounts.SessionUser, error)

func (f resolverFunc) ResolveAccountUID(ctx context.Context, uid int) (accounts.SessionUser, error) {
	return f(ctx, uid)
}

// Root or a peer without credentials never reaches the assistant socket when
// it is wired right, so it is a fault to log, not an unknown member.
func TestAssistantPeerThatIsNoMemberIsAWiringFault(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"no credentials": context.Background(),
		"root":           context.WithValue(context.Background(), peerUIDKey{}, 0),
	} {
		t.Run(name, func(t *testing.T) {
			asked := false
			resolver := resolverFunc(func(context.Context, int) (accounts.SessionUser, error) {
				asked = true
				return accounts.SessionUser{UserID: "user:root"}, nil
			})
			logger := slog.New(slog.DiscardHandler)
			handler := authenticateAssistant(resolver, logger, NewGate(time.Minute, logger), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("the request reached the photo API")
			}))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/photos/libraries", nil).WithContext(ctx))
			if recorder.Code != http.StatusServiceUnavailable || asked {
				t.Fatalf("status = %d, Host Agent asked = %v; want 503 without asking", recorder.Code, asked)
			}
		})
	}
}
