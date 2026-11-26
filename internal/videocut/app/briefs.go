package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/zhongwater123/A-NAS/internal/videocut/db"
	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

// What the videocut tools do (docs/architecture/montage-center.md, MCP 服务器):
// the briefs give a step's data and exact schema, the submissions check an
// answer against them and keep the first good one. Every call names the
// job and the round's token, and answers only the member whose round it is.

// Planning steps as the tools name them.
const (
	StepOutline = "pick"
	StepShots   = "shots"
)

// Limits of an answer's text (docs/architecture/montage-center.md, 校验).
// The prompt (plan-v2.md) says the same: 120 characters a field, 1,500 in
// all of an outline, card and beat names not counted. Bytes undercounted
// Chinese threefold: 2 KiB was about 680 characters, less than an outline of
// four parts needs.
const (
	maxAnswerRunes = 120
	maxAnswerText  = 1500
	maxAnswerBytes = 64 << 10
)

// Tool statuses.
const (
	ToolAccepted     = "accepted"
	ToolRejected     = "rejected"
	ToolCancelled    = "cancelled"
	ToolRoundOver    = "round_over"
	ToolOutlineFirst = "outline_first"
	// ToolNextPlan: a 方案's shots were taken and the job wants another; the
	// model starts over from the outline.
	ToolNextPlan = "next_plan"
)

var toolMessages = map[string]string{
	ToolAccepted:     "已收下",
	ToolCancelled:    "任务已取消",
	ToolRoundOver:    "这一轮已结束",
	ToolOutlineFirst: "先交大纲",
	ToolNextPlan:     "这一版已收下，接着做下一版",
}

func toolAnswer(status string, reasons ...string) domain.ToolAnswer {
	return domain.ToolAnswer{Status: status, Message: toolMessages[status], Reasons: reasons}
}

// roundCall is the round a tool call works on: the member's, for the job,
// planning the first step's input key the job still has, and how many 方案
// the job makes.
type roundCall struct {
	roundID, jobID, pickKey string
	plans                   int
}

// standing finds the member's round for jobID with token, as it stands now.
// A job cancelled, or a round that is over, answers the model so, as a
// ToolAnswer: the round ended, was abandoned or ran out of time, the member
// planned again (another epoch), or the job's inputs are no longer the ones
// the round was claimed for, so that what it hands back answers a step the
// job no longer has.
func (s *Store) standing(ctx context.Context, q queryer, owner, jobID, token string) (roundCall, *domain.ToolAnswer, error) {
	if token == "" || len(token) > 64 {
		return roundCall{}, nil, domain.ErrNotFound
	}
	call := roundCall{jobID: jobID}
	r, err := q.RoundStanding(ctx, owner, jobID, token)
	call.roundID, call.pickKey, call.plans = r.ID, r.InputKey, r.Plans
	state, epoch, current, cancelled, pickKey := r.State, r.Epoch, r.JobEpoch, r.Cancelled, r.PickKey
	if errors.Is(err, sql.ErrNoRows) {
		return roundCall{}, nil, domain.ErrNotFound
	}
	if err != nil {
		return roundCall{}, nil, err
	}
	status := ""
	switch {
	case cancelled:
		status = ToolCancelled
	case state != RoundClaimed && state != RoundStarted, epoch != current, !pickKey.Valid || pickKey.String != call.pickKey:
		status = ToolRoundOver
	}
	if status != "" {
		answer := toolAnswer(status)
		return roundCall{}, &answer, nil
	}
	return call, nil, nil
}

// roundFor is the standing round of a tool call. Each call shows that its
// model is still planning: the round's time is counted from the last one
// (SweepRounds), as one round plans both 方案, eight calls or more.
func (s *Store) roundFor(ctx context.Context, owner, jobID, token string) (roundCall, *domain.ToolAnswer, error) {
	call, over, err := s.standing(ctx, s.db, owner, jobID, token)
	if err != nil || over != nil {
		return call, over, err
	}
	err = s.db.MarkRoundCalled(ctx, call.roundID, s.now())
	return call, nil, err
}

// storedInput is what the job's steps stored for planning: the catalog's
// cards, the excerpt and the member's settings.
func (s *Store) storedInput(ctx context.Context, jobID string) (domain.PlanInput, error) {
	input := domain.PlanInput{JobID: jobID}
	var err error
	if input.Settings, err = jobSettings(ctx, s.db, jobID); err != nil {
		return domain.PlanInput{}, err
	}
	catalogJSON, err := s.db.DoneCatalog(ctx, jobID)
	if err != nil {
		return domain.PlanInput{}, fmt.Errorf("the job has no catalog yet: %w", err)
	}
	songJSON, err := s.db.DoneExcerpt(ctx, jobID)
	if err != nil {
		return domain.PlanInput{}, fmt.Errorf("the job has no excerpt yet: %w", err)
	}
	var catalog catalogResult
	if err := json.Unmarshal([]byte(catalogJSON), &catalog); err != nil {
		return domain.PlanInput{}, err
	}
	var excerpt domain.Excerpt
	if err := json.Unmarshal([]byte(songJSON), &excerpt); err != nil {
		return domain.PlanInput{}, err
	}
	input.Cards = make([]videocutcompute.CatalogCard, 0, len(catalog.Catalog))
	for _, id := range catalog.Catalog {
		input.Cards = append(input.Cards, catalog.Cards[id])
	}
	input.Music = excerpt.Music
	return input, nil
}

// briefOf is the brief of a step for its input key: the one kept, or one
// built by the compute Worker from input and kept. The Worker answers one
// call at a time, behind the jobs' own work, so a brief is built once: the
// job's step builds it before a round can ask (planned), and the tools read
// what is kept, building the second step's only when the model asks for it
// before the job came to that step.
func (s *Store) briefOf(ctx context.Context, jobID, step, key string, input func() (domain.PlanInput, error)) (videocutcompute.PlanBrief, error) {
	var brief videocutcompute.PlanBrief
	kept, err := s.db.PlanBrief(ctx, jobID, step, key)
	if err == nil && json.Unmarshal([]byte(kept), &brief) == nil {
		return brief, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return videocutcompute.PlanBrief{}, err
	}
	in, err := input()
	if err != nil {
		return videocutcompute.PlanBrief{}, err
	}
	request := videocutcompute.PlanBriefRequest{Step: step, Cards: in.Cards, Music: in.Music, Ask: in.Settings.Ask,
		Chronological: in.Settings.Chronological, Picked: in.Picked}
	if recut := in.Settings.Recut; recut != nil {
		request.Change = recut.Change
		if request.Base, err = alignBase(recut.Base, recut.Cards, in.Cards); err != nil {
			return videocutcompute.PlanBrief{}, err
		}
	}
	brief, err = s.jobCompute.PlanBrief(ctx, request)
	if err != nil {
		return videocutcompute.PlanBrief{}, err
	}
	if want := templateOf(in.Settings); brief.Template != want {
		return videocutcompute.PlanBrief{}, fmt.Errorf("the compute Worker briefs for %q, VideoCut for %q", brief.Template, want)
	}
	encoded, err := json.Marshal(brief)
	if err != nil {
		return videocutcompute.PlanBrief{}, err
	}
	// The same inputs give the same brief, whoever built it first.
	err = s.db.KeepPlanBrief(ctx, jobID, step, key, string(encoded))
	return brief, err
}

// shotsTaken reports whether both steps of a 方案 have their answers.
func (s *Store) shotsTaken(ctx context.Context, call roundCall, plan int) (bool, error) {
	pick := planKey(call.pickKey, plan)
	outline, taken, err := s.planAnswer(ctx, call.jobID, StepOutline, pick)
	if err != nil || !taken {
		return false, err
	}
	_, taken, err = s.planAnswer(ctx, call.jobID, StepShots, shotsKey(pick, outline))
	return taken, err
}

// currentPlan is the 方案 a round works on: the first not saved whose second
// step has no answer yet; past the last, the last. It reads the answers
// kept, not the job's plans: the job may not have come to an answer yet when
// the model hands in the next. A 方案 saved is settled, also when the job's
// inputs changed since and its answers are under other keys (runSteps).
func (s *Store) currentPlan(ctx context.Context, call roundCall) (int, error) {
	saved, err := s.db.SavedPlans(ctx, call.jobID)
	if err != nil {
		return 0, err
	}
	for plan := 1; plan < call.plans; plan++ {
		if saved[plan] {
			continue
		}
		if taken, err := s.shotsTaken(ctx, call, plan); err != nil || !taken {
			return plan, err
		}
	}
	return call.plans, nil
}

// stepBrief is a step's brief for a round and a 方案, and the step's input
// key. The second step's are those of the outline taken; before one is, the
// model is told to hand the outline in first. The outline's brief is the
// same for every 方案 and kept under the first's key (planned).
func (s *Store) stepBrief(ctx context.Context, call roundCall, plan int, step string) (videocutcompute.PlanBrief, string, *domain.ToolAnswer, error) {
	key := planKey(call.pickKey, plan)
	briefKey := call.pickKey
	var picked json.RawMessage
	if step == StepShots {
		outline, taken, err := s.planAnswer(ctx, call.jobID, StepOutline, key)
		if err != nil {
			return videocutcompute.PlanBrief{}, "", nil, err
		}
		if !taken {
			answer := toolAnswer(ToolOutlineFirst)
			return videocutcompute.PlanBrief{}, "", &answer, nil
		}
		picked, key = outline, shotsKey(key, outline)
		briefKey = key
	}
	brief, err := s.briefOf(ctx, call.jobID, step, briefKey, func() (domain.PlanInput, error) {
		input, err := s.storedInput(ctx, call.jobID)
		input.Picked = picked
		return input, err
	})
	return brief, key, nil, err
}

// withEarlier adds to a later 方案's outline data the outlines taken before
// it, which it should differ from, and which 方案 it is. They are the job's
// state now, so they are added as the brief is given, not kept with it.
func (s *Store) withEarlier(ctx context.Context, call roundCall, plan int, data json.RawMessage) (json.RawMessage, error) {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	earlier := []json.RawMessage{}
	for before := 1; before < plan; before++ {
		outline, taken, err := s.planAnswer(ctx, call.jobID, StepOutline, planKey(call.pickKey, before))
		if err != nil {
			return nil, err
		}
		if taken {
			earlier = append(earlier, outline)
		}
	}
	encoded, err := json.Marshal(earlier)
	if err != nil {
		return nil, err
	}
	fields["plan"], fields["earlierOutlines"] = json.RawMessage(fmt.Sprint(plan)), encoded
	return json.Marshal(fields)
}

// Brief is a step's data and exact schema for owner's round on jobID.
func (s *Store) Brief(ctx context.Context, owner, jobID, token, step string) (any, error) {
	call, over, err := s.roundFor(ctx, owner, jobID, token)
	if err != nil {
		return nil, err
	}
	if over != nil {
		return over, nil
	}
	plan, err := s.currentPlan(ctx, call)
	if err != nil {
		return nil, err
	}
	brief, _, notYet, err := s.stepBrief(ctx, call, plan, step)
	if err != nil {
		return nil, err
	}
	if notYet != nil {
		return notYet, nil
	}
	data := brief.Data
	if step == StepOutline && plan > 1 {
		if data, err = s.withEarlier(ctx, call, plan, data); err != nil {
			return nil, err
		}
	}
	return map[string]json.RawMessage{"data": data, "schema": brief.Schema}, nil
}

// Submit checks the answer to a step of owner's round on jobID and keeps the
// first good one for the step's input key; the job's step takes it from
// there (planned). The answer is for the 方案 the round works on
// (currentPlan); shots taken with another 方案 to plan answer ToolNextPlan.
// Only a round still standing when the answer is written has it kept: one
// that ended meanwhile, or whose job changed its inputs or was planned
// again, is told the round is over and its answer is dropped. Three refusals
// in a row end the round (refuse).
func (s *Store) Submit(ctx context.Context, owner, jobID, token, step string, answer json.RawMessage) (domain.ToolAnswer, error) {
	call, over, err := s.roundFor(ctx, owner, jobID, token)
	if err != nil {
		return domain.ToolAnswer{}, err
	}
	if over != nil {
		return *over, nil
	}
	plan, err := s.currentPlan(ctx, call)
	if err != nil {
		return domain.ToolAnswer{}, err
	}
	brief, key, notYet, err := s.stepBrief(ctx, call, plan, step)
	if err != nil {
		return domain.ToolAnswer{}, err
	}
	if notYet != nil {
		if step == StepShots && plan > 1 {
			// Shots with no outline of this 方案 yet: those of the 方案
			// before were taken, as a call tried again finds.
			return toolAnswer(ToolNextPlan), nil
		}
		return *notYet, nil
	}
	taken := ToolAccepted
	if step == StepShots && plan < call.plans {
		taken = ToolNextPlan
	}
	if _, kept, err := s.planAnswer(ctx, jobID, step, key); err != nil || kept {
		return toolAnswer(taken), err
	}
	if reasons := checkAnswer(brief.Schema, answer, step); len(reasons) > 0 {
		return s.refuse(ctx, owner, jobID, token, reasons)
	}
	// As the job's step stores a result (runSteps): the second step's key
	// is worked out from these bytes on both sides (shotsKey).
	kept, err := json.Marshal(answer)
	if err != nil {
		return domain.ToolAnswer{}, err
	}
	var late *domain.ToolAnswer
	first := false
	err = s.db.Update(ctx, func(tx *db.Tx) error {
		if _, late, err = s.standing(ctx, tx, owner, jobID, token); err != nil || late != nil {
			return err
		}
		// The first answer for the key stays: one that came meanwhile, as
		// from a tool call tried again, is not replaced.
		inserted, err := tx.KeepPlanAnswer(ctx, jobID, step, key, call.roundID, string(kept), s.now())
		if err != nil {
			return err
		}
		if !inserted {
			return nil
		}
		first = true
		if err := tx.ResetPlanRejections(ctx, jobID); err != nil {
			return err
		}
		return s.dueNow(ctx, tx, jobID)
	})
	if err != nil {
		return domain.ToolAnswer{}, err
	}
	if late != nil {
		return *late, nil
	}
	if first {
		s.logger.InfoContext(ctx, "planning step taken", "job", jobID, "round", call.roundID, "step", step, "plan", plan)
		s.wakeJobs()
	}
	return toolAnswer(taken), nil
}

// refuse counts a refused answer; the third in a row ends the round, which
// counts as one tried, and the job plans again in a round of its own
// (awaitPlan) with the count started over. As a good answer is kept, a
// refusal counts only for a round still standing when it is written, in the
// same transaction: checking the answer may have waited for the compute
// Worker, and a round that ended meanwhile, or whose job was planned again,
// must not count against the planning that follows it. Such a round is told
// it is over.
func (s *Store) refuse(ctx context.Context, owner, jobID, token string, reasons []string) (domain.ToolAnswer, error) {
	var late *domain.ToolAnswer
	var rejections int
	err := s.db.Update(ctx, func(tx *db.Tx) error {
		call, over, err := s.standing(ctx, tx, owner, jobID, token)
		if err != nil || over != nil {
			late = over
			return err
		}
		if rejections, err = tx.CountPlanRejection(ctx, jobID); err != nil {
			return err
		}
		if rejections < maxPlanRejections {
			return nil
		}
		if err := tx.EndRoundByID(ctx, call.roundID, s.now(), "答案连续 3 次不合格"); err != nil {
			return err
		}
		if err := tx.ResetPlanRejections(ctx, jobID); err != nil {
			return err
		}
		return s.dueNow(ctx, tx, jobID)
	})
	if err != nil {
		return domain.ToolAnswer{}, err
	}
	if late != nil {
		return *late, nil
	}
	if rejections >= maxPlanRejections {
		s.wakeJobs()
		reasons = append(reasons, "连续 3 次不合格，这一轮到此为止")
	}
	return toolAnswer(ToolRejected, reasons...), nil
}

// checkAnswer checks an answer against its step's exact schema and the
// limits of its text, and says what is wrong in words the model can act on.
func checkAnswer(schema, answer json.RawMessage, step string) []string {
	if len(answer) == 0 || len(answer) > maxAnswerBytes {
		return []string{fmt.Sprintf("答案为空或超过 %d 字节", maxAnswerBytes)}
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(answer))
	if err != nil {
		return []string{"答案不是 JSON：" + err.Error()}
	}
	compiled, err := compileSchema(schema)
	if err != nil {
		return []string{"这一步的 Schema 无法使用：" + err.Error()}
	}
	if err := compiled.Validate(value); err != nil {
		var reasons []string
		if invalid, ok := errors.AsType[*jsonschema.ValidationError](err); ok {
			for _, cause := range leafCauses(invalid) {
				reasons = append(reasons, fmt.Sprintf("%s：%s", "/"+strings.Join(cause.InstanceLocation, "/"), cause.Error()))
				if len(reasons) == 10 {
					break
				}
			}
		}
		if len(reasons) == 0 {
			reasons = []string{err.Error()}
		}
		return reasons
	}
	var reasons []string
	total := 0
	walkStrings(value, func(path []string, text string) {
		if isEnumerated(path) {
			return
		}
		n := utf8.RuneCountInString(text)
		total += n
		if n > maxAnswerRunes {
			reasons = append(reasons, fmt.Sprintf("/%s：%d 字，超过 %d 字", strings.Join(path, "/"), n, maxAnswerRunes))
		}
	})
	if step == StepOutline && total > maxAnswerText {
		reasons = append(reasons, fmt.Sprintf("文字合计 %d 字，超过 %d 字", total, maxAnswerText))
	}
	return reasons
}

// isEnumerated: enumerated values, such as card and beat names, are not text.
func isEnumerated(path []string) bool {
	for _, p := range path {
		if p == "cards" || p == "beats" {
			return true
		}
	}
	return false
}

func compileSchema(schema json.RawMessage) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("brief.json", doc); err != nil {
		return nil, err
	}
	return compiler.Compile("brief.json")
}

func leafCauses(err *jsonschema.ValidationError) []*jsonschema.ValidationError {
	if len(err.Causes) == 0 {
		return []*jsonschema.ValidationError{err}
	}
	var leaves []*jsonschema.ValidationError
	for _, cause := range err.Causes {
		leaves = append(leaves, leafCauses(cause)...)
	}
	return leaves
}

func walkStrings(value any, visit func(path []string, text string)) {
	var walk func(path []string, v any)
	walk = func(path []string, v any) {
		switch v := v.(type) {
		case string:
			visit(path, v)
		case map[string]any:
			for key, item := range v {
				walk(append(append([]string{}, path...), key), item)
			}
		case []any:
			for i, item := range v {
				walk(append(append([]string{}, path...), fmt.Sprint(i)), item)
			}
		}
	}
	walk(nil, value)
}
