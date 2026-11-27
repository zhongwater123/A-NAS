package rootbroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/atomicfile"
	"github.com/zhongwater123/A-NAS/internal/filebroker/wire"
)

// A planning grant (docs/architecture/montage-center.md, 规划授权): while
// the member is signed in and does something the montage center may plan
// for, the Product Service asks the broker, behind the member's session,
// to let their VNAS plan in their absence. The broker records when, and how
// many rounds; only root's planner reads it (ai/assistant/montage-planner.py) and
// hands a round to the member's planning instance. Registering again replaces it.
// ADR 0024 decision 10 keeps background services from driving a member's
// assistant on their own: the grant is the member's, left while present.
//
// The grants are in the Host Agent's own state directory, which only root
// enters (/var/lib/a-nas-host/assistant/plans), never under a service's: a
// service that owns a directory above them could put grants of its own
// making there, or a link that takes root's write elsewhere. The broker
// writes only into a directory that is its own user's and nobody else's to
// change, and the planner reads only such grants.

// planGrantRounds is how many rounds of planning one grant allows.
const planGrantRounds = 5

// planGrant is what the planner reads.
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
	// As the planner and "run now" tell: no key, no model for any member.
	if s.config.AssistantOn != nil && !s.config.AssistantOn() {
		return wire.Response{Plan: wire.PlanDeviceOff}
	}
	// As the planner tells an account with an assistant: its token file.
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
	if err := ownDirectory(s.config.AssistantPlans); err != nil {
		return err
	}
	encoded, err := json.Marshal(planGrant{IssuedAt: time.Now().UTC().Format(time.RFC3339), Rounds: planGrantRounds})
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(s.config.AssistantPlans, username), encoded, 0o600)
}

// ownDirectory accepts path only as a directory itself, not a link to one,
// that belongs to this process's user and that nobody else may write to.
func ownDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	switch {
	case !info.IsDir():
		return fmt.Errorf("%s is not a directory", path)
	case !ok || int(stat.Uid) != os.Geteuid():
		return fmt.Errorf("%s is not the broker's own", path)
	case info.Mode().Perm()&0o022 != 0:
		return fmt.Errorf("%s can be written by others", path)
	}
	return nil
}
