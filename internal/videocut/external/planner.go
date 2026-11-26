package external

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
)

// FixedPlanner stands in for the planner until the AI assistant plans
// (M9, #560): it answers every job with pick.json and shots.json from Dir,
// answers written for one set of videos, such as the prototype's. It is for
// the development machine and the system test, never a product's choice.
type FixedPlanner struct{ Dir string }

func (p FixedPlanner) read(name string) (json.RawMessage, error) {
	answer, err := os.ReadFile(filepath.Join(p.Dir, name))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrPlanUnknown, err)
	}
	if !json.Valid(answer) {
		return nil, fmt.Errorf("%w: %s is not JSON", domain.ErrPlanInvalid, name)
	}
	return answer, nil
}

// Pick answers pick.json.
func (p FixedPlanner) Pick(context.Context, domain.PlanInput) (json.RawMessage, error) {
	return p.read("pick.json")
}

// Shots answers shots.json: {answer, slots}.
func (p FixedPlanner) Shots(context.Context, domain.PlanInput) (json.RawMessage, error) {
	return p.read("shots.json")
}

// PlanMarkers keeps the planning markers in Dir (/run/a-nas-montage/plans):
// one empty file per member whose job wants a round, named by the account.
type PlanMarkers struct{ Dir string }

func (m PlanMarkers) path(username string) string { return filepath.Join(m.Dir, username) }

// Leave leaves username's marker; a name the planner would not accept
// leaves none.
func (m PlanMarkers) Leave(username string) error {
	if !domain.AccountName.MatchString(username) {
		return nil
	}
	file, err := os.OpenFile(m.path(username), os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	return file.Close()
}

// Has reports whether username has a marker.
func (m PlanMarkers) Has(username string) bool {
	if !domain.AccountName.MatchString(username) {
		return false
	}
	_, err := os.Stat(m.path(username))
	return err == nil
}

// Keep takes away the markers of members not in wanted and leaves those of
// the members in it.
func (m PlanMarkers) Keep(wanted map[string]bool) error {
	entries, err := os.ReadDir(m.Dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !wanted[entry.Name()] {
			_ = os.Remove(filepath.Join(m.Dir, entry.Name()))
		}
	}
	for username := range wanted {
		if err := m.Leave(username); err != nil {
			return err
		}
	}
	return nil
}
