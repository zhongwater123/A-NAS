package rootbroker

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/filebroker/wire"
)

// A planning grant (docs/architecture/montage-center.md, 规划授权): while
// the member is signed in and does something the montage center may plan
// for, the Product Service asks the broker, behind the member's session,
// to let their VNAS plan in their absence. The broker records when, and how
// many rounds; only root's waker reads it (ai/assistant/cron-waker.py) and
// starts the member's cron unit for a round. Registering again replaces it.
// ADR 0024 decision 10 keeps background services from driving a member's
// assistant on their own: the grant is the member's, left while present.

// planGrantRounds is how many rounds of planning one grant allows.
const planGrantRounds = 5

// planGrant is what the waker reads.
type planGrant struct {
	IssuedAt string `json:"issuedAt"`
	Rounds   int    `json:"rounds"`
}

// serveAssistantPlan registers a planning grant for the member behind the
// session, or says why they have no VNAS to plan with.
func (s *Server) serveAssistantPlan(req wire.Request) wire.Response {
	identity, refused := s.identify(context.Background(), req.Token)
	if refused != nil {
		return wire.Response{Error: refused}
	}
	if s.config.AssistantPlans == "" || s.config.AssistantTokens == "" {
		return wire.Response{Plan: wire.PlanDeviceOff}
	}
	// As the waker and "run now" tell: no key, no model for any member.
	if s.config.AssistantOn != nil && !s.config.AssistantOn() {
		return wire.Response{Plan: wire.PlanDeviceOff}
	}
	// As the waker tells an account with an assistant: its token file.
	if !accountid.ValidAssistantAccount(identity.Username) {
		return wire.Response{Plan: wire.PlanNoAssistant}
	}
	info, err := os.Lstat(filepath.Join(s.config.AssistantTokens, identity.Username+".env"))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return wire.Response{Plan: wire.PlanNoAssistant}
	case err != nil:
		s.config.Logger.Error("assistant token unreadable", "username", identity.Username, "error", err)
		return wire.Response{Error: &wire.Error{Code: wire.CodeUnavailable, Message: "the planning grant cannot be registered right now"}}
	case !info.Mode().IsRegular():
		return wire.Response{Plan: wire.PlanNoAssistant}
	}
	if err := s.writePlanGrant(identity.Username); err != nil {
		s.config.Logger.Error("planning grant not written", "username", identity.Username, "error", err)
		return wire.Response{Error: &wire.Error{Code: wire.CodeUnavailable, Message: "the planning grant cannot be registered right now"}}
	}
	s.config.Logger.Info("planning grant registered", "username", identity.Username)
	return wire.Response{Plan: wire.PlanRegistered}
}

// writePlanGrant replaces the member's grant whole, on disk before its name.
func (s *Server) writePlanGrant(username string) error {
	if err := os.MkdirAll(s.config.AssistantPlans, 0o700); err != nil {
		return err
	}
	encoded, err := json.Marshal(planGrant{IssuedAt: time.Now().UTC().Format(time.RFC3339), Rounds: planGrantRounds})
	if err != nil {
		return err
	}
	partial, err := os.CreateTemp(s.config.AssistantPlans, "."+username+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(partial.Name()) }()
	if _, err := partial.Write(encoded); err != nil {
		_ = partial.Close()
		return err
	}
	if err := errors.Join(partial.Chmod(0o600), partial.Sync(), partial.Close()); err != nil {
		return err
	}
	return os.Rename(partial.Name(), filepath.Join(s.config.AssistantPlans, username))
}
