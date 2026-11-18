package montage

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

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// What the montage tools do (docs/architecture/montage-center.md, MCP 服务器):
// the briefs give a step's data and exact schema, the submissions check an
// answer against them and keep the first good one. Every call names the
// job and the round's token, and answers only the member whose round it is.

// Planning steps as the tools name them.
const (
	StepOutline = "pick"
	StepShots   = "shots"
)

// Limits of an answer's text (docs/architecture/montage-center.md, 校验).
const (
	maxAnswerRunes = 80
	maxAnswerText  = 2 << 10
	maxAnswerBytes = 64 << 10
)

// Tool statuses.
const (
	ToolAccepted     = "accepted"
	ToolRejected     = "rejected"
	ToolCancelled    = "cancelled"
	ToolRoundOver    = "round_over"
	ToolOutlineFirst = "outline_first"
)

// ToolAnswer is what a submission, or a brief that cannot be given,
// answers the model. Message is for the model, in the template's words.
type ToolAnswer struct {
	Status  string   `json:"status"`
	Message string   `json:"message,omitempty"`
	Reasons []string `json:"reasons,omitempty"`
}

var toolMessages = map[string]string{
	ToolAccepted:     "已收下",
	ToolCancelled:    "任务已取消",
	ToolRoundOver:    "这一轮已结束",
	ToolOutlineFirst: "先交大纲",
}

func toolAnswer(status string, reasons ...string) ToolAnswer {
	return ToolAnswer{Status: status, Message: toolMessages[status], Reasons: reasons}
}

// planContext is what a brief is made of: the round, and the job's stored
// catalog, excerpt and settings.
type planContext struct {
	roundID    string
	catalogKey string
	request    PlanBriefRequest
}

// roundFor finds the member's round for jobID with token. A round over,
// or a job cancelled, answers the model so, as a ToolAnswer.
func (s *Store) roundFor(ctx context.Context, owner, jobID, token string) (planContext, *ToolAnswer, error) {
	if token == "" || len(token) > 64 {
		return planContext{}, nil, ErrNotFound
	}
	var roundID, state string
	err := s.db.QueryRowContext(ctx, `SELECT id, state FROM rounds WHERE job_id = ? AND owner_user_id = ? AND attempt_token = ?`,
		jobID, owner, token).Scan(&roundID, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return planContext{}, nil, ErrNotFound
	}
	if err != nil {
		return planContext{}, nil, err
	}
	if s.cancelRequested(ctx, jobID) {
		answer := toolAnswer(ToolCancelled)
		return planContext{}, &answer, nil
	}
	if state != RoundClaimed && state != RoundStarted {
		answer := toolAnswer(ToolRoundOver)
		return planContext{}, &answer, nil
	}
	pc := planContext{roundID: roundID}
	var settings string
	if err := s.db.QueryRowContext(ctx, "SELECT settings_json FROM jobs WHERE id = ?", jobID).Scan(&settings); err != nil {
		return planContext{}, nil, err
	}
	var js JobSettings
	if err := json.Unmarshal([]byte(settings), &js); err != nil {
		return planContext{}, nil, err
	}
	var catalogJSON, songJSON string
	if err := s.db.QueryRowContext(ctx, "SELECT input_key, result_json FROM steps WHERE job_id = ? AND name = 'catalog' AND state = 'done'",
		jobID).Scan(&pc.catalogKey, &catalogJSON); err != nil {
		return planContext{}, nil, fmt.Errorf("the job has no catalog yet: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT result_json FROM steps WHERE job_id = ? AND name = 'song' AND state = 'done'",
		jobID).Scan(&songJSON); err != nil {
		return planContext{}, nil, fmt.Errorf("the job has no excerpt yet: %w", err)
	}
	var catalog catalogResult
	if err := json.Unmarshal([]byte(catalogJSON), &catalog); err != nil {
		return planContext{}, nil, err
	}
	var excerpt Excerpt
	if err := json.Unmarshal([]byte(songJSON), &excerpt); err != nil {
		return planContext{}, nil, err
	}
	cards := make([]Card, 0, len(catalog.Catalog))
	for _, id := range catalog.Catalog {
		cards = append(cards, catalog.Cards[id])
	}
	pc.request = PlanBriefRequest{Cards: cards, Music: excerpt.Music, Ask: js.Ask, Chronological: js.Chronological}
	return pc, nil, nil
}

// brief is a step's brief for the job, with the outline taken for the
// second step.
func (s *Store) brief(ctx context.Context, jobID, step string, pc planContext) (PlanBrief, *ToolAnswer, error) {
	request := pc.request
	request.Step = step
	if step == StepShots {
		picked, ok, err := s.accepted(ctx, jobID, StepOutline, pc.catalogKey)
		if err != nil {
			return PlanBrief{}, nil, err
		}
		if !ok {
			answer := toolAnswer(ToolOutlineFirst)
			return PlanBrief{}, &answer, nil
		}
		request.Picked = picked
	}
	brief, err := s.jobCompute.PlanBrief(ctx, request)
	if err != nil {
		return PlanBrief{}, nil, err
	}
	if brief.Template != PlanTemplate {
		return PlanBrief{}, nil, fmt.Errorf("the compute Worker briefs for %q, the montage center for %q", brief.Template, PlanTemplate)
	}
	return brief, nil, nil
}

// Brief is a step's data and exact schema for owner's round on jobID.
func (s *Store) Brief(ctx context.Context, owner, jobID, token, step string) (any, error) {
	pc, over, err := s.roundFor(ctx, owner, jobID, token)
	if err != nil || over != nil {
		return over, err
	}
	brief, answer, err := s.brief(ctx, jobID, step, pc)
	if err != nil || answer != nil {
		return answer, err
	}
	return map[string]json.RawMessage{"data": brief.Data, "schema": brief.Schema}, nil
}

// Submit checks the answer to a step of owner's round on jobID and keeps
// the first good one. Three refusals in a row and the member is asked.
func (s *Store) Submit(ctx context.Context, owner, jobID, token, step string, answer json.RawMessage) (ToolAnswer, error) {
	pc, over, err := s.roundFor(ctx, owner, jobID, token)
	if err != nil {
		return ToolAnswer{}, err
	}
	if over != nil {
		return *over, nil
	}
	if _, taken, err := s.accepted(ctx, jobID, step, pc.catalogKey); err != nil || taken {
		return toolAnswer(ToolAccepted), err
	}
	var rejections int
	if err := s.db.QueryRowContext(ctx, "SELECT plan_rejections FROM jobs WHERE id = ?", jobID).Scan(&rejections); err != nil {
		return ToolAnswer{}, err
	}
	if rejections >= maxPlanRejections {
		return toolAnswer(ToolRoundOver), nil
	}
	brief, notYet, err := s.brief(ctx, jobID, step, pc)
	if err != nil || notYet != nil {
		if notYet != nil {
			return *notYet, nil
		}
		return ToolAnswer{}, err
	}
	if reasons := checkAnswer(brief.Schema, answer, step); len(reasons) > 0 {
		return s.refuse(ctx, jobID, reasons)
	}
	kept := answer
	if step == StepShots {
		turned, err := s.jobCompute.ShotsAnswer(ctx, answer, pc.request.Picked, brief.Pictures)
		if err != nil {
			return ToolAnswer{}, err
		}
		if kept, err = json.Marshal(map[string]json.RawMessage{"answer": turned, "slots": brief.Slots}); err != nil {
			return ToolAnswer{}, err
		}
	}
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO plans(job_id, step, round_id, catalog_key, answer_json, accepted_at) VALUES(?, ?, ?, ?, ?, ?)",
			jobID, step, pc.roundID, pc.catalogKey, string(kept), sqlitedb.FormatTime(s.now())); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE jobs SET plan_rejections = 0 WHERE id = ?", jobID)
		return err
	})
	if err != nil {
		return ToolAnswer{}, err
	}
	s.logger.InfoContext(ctx, "planning step taken", "job", jobID, "round", pc.roundID, "step", step)
	s.wakeJobs()
	return toolAnswer(ToolAccepted), nil
}

// refuse counts a refused answer; the third in a row is the last this
// round may give.
func (s *Store) refuse(ctx context.Context, jobID string, reasons []string) (ToolAnswer, error) {
	var rejections int
	if err := s.db.QueryRowContext(ctx, "UPDATE jobs SET plan_rejections = plan_rejections + 1 WHERE id = ? RETURNING plan_rejections",
		jobID).Scan(&rejections); err != nil {
		return ToolAnswer{}, err
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
		n := utf8.RuneCountInString(text)
		total += len(text)
		if n > maxAnswerRunes && !isEnumerated(path) {
			reasons = append(reasons, fmt.Sprintf("/%s：%d 字，超过 %d 字", strings.Join(path, "/"), n, maxAnswerRunes))
		}
	})
	if step == StepOutline && total > maxAnswerText {
		reasons = append(reasons, fmt.Sprintf("文字合计 %d 字节，超过 %d 字节", total, maxAnswerText))
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
