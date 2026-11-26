package app

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
func (p FixedPlanner) Pick(context.Context, PlanInput) (json.RawMessage, error) {
	return p.read("pick.json")
}

// Shots answers shots.json: {answer, slots}.
func (p FixedPlanner) Shots(context.Context, PlanInput) (json.RawMessage, error) {
	return p.read("shots.json")
}
