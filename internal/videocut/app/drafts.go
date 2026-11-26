package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
	"github.com/zhongwater123/A-NAS/internal/videocut/db"
	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

// Editing a film after it was made (#596, ADR 0039;
// docs/architecture/montage-center.md, 出片后的二次编辑). A member edits a version slot by slot in a draft kept
// here: each step is checked against the rules, written as a revision, and
// can be undone. Nothing here calls a model.

// AnalysisChangedReason tells the member why a version analysed again can
// no longer be edited, when its draft is opened and on a save that found it.
const AnalysisChangedReason = "素材已按新模型重新分析，这一版不能再修改，可以用一句话重剪"

func refuse(format string, args ...any) error {
	return &domain.EditRefusal{Reason: fmt.Sprintf(format, args...)}
}

const (
	// keptRevisions is how many of a draft's revisions are kept, and so how
	// many steps can be undone.
	keptRevisions = 100
	// slowMost is how much the fill may slow a shot (fill.SLOW_MOST): a beat
	// whose card fills a slot that much slowed may be chosen for it.
	slowMost = 2.5
	// narrowBy: a video narrower than 16:9 by more than this is shown on a
	// blurred background (renderClip), and counts as 竖.
	narrowBy = 0.01
)

// narrow reports whether a video is narrower than the film.
func narrow(width, height int) bool {
	return width > 0 && height > 0 && float64(width)/float64(height) < 16.0/9.0-narrowBy
}

// --- what a version was cut from ---

// timelineClip is one shot of a version's timeline as the fill wrote it:
// the clip the render cuts, and the plan's part and picture it is of.
type timelineClip struct {
	domain.Clip
	Part    int `json:"part"`
	Picture int `json:"picture"`
}

// plannedPicture is a picture as the plan's second step answered it: the
// beats it listed.
type plannedPicture struct {
	Beats []string `json:"beats"`
}

type plannedDoc struct {
	Answer struct {
		Unmet json.RawMessage `json:"unmet"`
		Parts []struct {
			Name     string           `json:"name"`
			Pictures []plannedPicture `json:"pictures"`
		} `json:"parts"`
	} `json:"answer"`
}

type pickedDoc struct {
	Design  string          `json:"design"`
	Opening string          `json:"opening"`
	Ending  string          `json:"ending"`
	Unmet   json.RawMessage `json:"unmet"`
	Parts   []struct {
		Name    string `json:"name"`
		Summary string `json:"summary"`
		Scenes  []struct {
			Query string `json:"query"`
		} `json:"scenes"`
	} `json:"parts"`
}

type musicDoc struct {
	Parts []struct {
		Start float64 `json:"start"`
		End   float64 `json:"end"`
		Level string  `json:"level"`
		Label string  `json:"label"`
	} `json:"parts"`
}

// beatAbout is what a beat's card describes of it and of the card.
type beatAbout struct {
	ShotSize string `json:"shot_size"`
	What     string `json:"what"`
}

type cardAbout struct {
	Scene string      `json:"scene"`
	Beats []beatAbout `json:"beats"`
}

// editSource is what editing a version reads: its timeline, and its job's
// cards, plans, excerpt and videos.
type editSource struct {
	versionID, projectID, grantID, jobID string
	number, plan                         int
	// planJob and planNo are the job and 方案 the version was planned in:
	// its own, or for an edited version the one it came from.
	planJob string
	planNo  int
	// songTranscode: browsers do not play the version's song as it is kept.
	songTranscode bool
	// pickedRaw and plannedRaw are the plan's two steps as they were
	// answered, whole: picked and planned read only what editing shows.
	pickedRaw, plannedRaw json.RawMessage
	clips                 []timelineClip
	cards                 map[string]videocutcompute.CatalogCard
	about                 map[string]cardAbout
	catalog               []string
	videos                map[string]analysedVideo
	planned               plannedDoc
	picked                pickedDoc
	music                 musicDoc
	excerpt               domain.Excerpt
}

// versionSource reads what owner's version was cut from. A version saved
// before versions kept their timeline takes its job's fill, and keeps it.
func (s *Store) versionSource(ctx context.Context, owner, versionID string) (*editSource, error) {
	src := &editSource{versionID: versionID}
	var timeline sql.NullString
	var grantID sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT v.project_id, v.number, v.job_id, v.plan, v.timeline_json, p.grant_id
		FROM versions v JOIN projects p ON p.id = v.project_id WHERE v.id = ? AND p.owner_user_id = ? AND p.state = 'ready'`,
		versionID, owner).Scan(&src.projectID, &src.number, &src.jobID, &src.plan, &timeline, &grantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	src.grantID = grantID.String
	src.planJob, src.planNo = src.jobID, src.plan
	settings, err := jobSettings(ctx, s.db, src.jobID)
	if err != nil {
		return nil, err
	}
	if settings.Edit != nil {
		src.planJob, src.planNo = settings.Edit.Job, settings.Edit.Plan
	}
	if settings.SongID != "" {
		// A song gone since is not heard at all (OpenDraftSongAudio).
		if song, err := s.song(ctx, s.db, settings.SongID); err == nil {
			src.songTranscode = song.contentType == ""
		}
	}
	kept, ok, err := s.loadPlan(ctx, src.planJob, src.planNo)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.ErrNotEditable
	}
	stored := timeline.Valid
	if !stored {
		fill := planStep("fill", src.plan)
		own, err := s.doneResults(ctx, src.jobID, []string{fill})
		if err != nil {
			return nil, err
		}
		timeline.String = string(own[fill])
	}
	var filled timelineResult
	src.excerpt, src.pickedRaw, src.plannedRaw = kept.excerpt, kept.picked, kept.planned
	if timeline.String == "" || json.Unmarshal([]byte(timeline.String), &filled) != nil || len(filled.Clips) == 0 ||
		json.Unmarshal(src.excerpt.Music, &src.music) != nil ||
		json.Unmarshal(src.pickedRaw, &src.picked) != nil || json.Unmarshal(src.plannedRaw, &src.planned) != nil {
		return nil, domain.ErrNotEditable
	}
	for _, raw := range filled.Clips {
		var clip timelineClip
		if err := json.Unmarshal(raw, &clip); err != nil {
			return nil, domain.ErrNotEditable
		}
		src.clips = append(src.clips, clip)
	}
	src.cards, src.catalog = kept.catalog.Cards, kept.catalog.Catalog
	src.about, src.videos = map[string]cardAbout{}, map[string]analysedVideo{}
	for id, card := range src.cards {
		var about cardAbout
		if json.Unmarshal(card.Answer, &about) != nil || len(about.Beats) != len(card.Times) {
			return nil, domain.ErrNotEditable
		}
		src.about[id] = about
	}
	for _, video := range kept.analysis.Videos {
		src.videos[video.AssetID] = video
	}
	for _, clip := range src.clips {
		// Each shot is shown by its beat's description (view): a beat its
		// card does not have cannot be.
		if _, _, known := src.beat(clip.Beat); !known || !src.pictureKnown(clip.Part, clip.Picture) {
			return nil, domain.ErrNotEditable
		}
		// A card numbered anew names another video's footage (redoneSince).
		if card, ok := src.cards[clip.Card]; !ok || card.Video != clip.Video {
			return nil, domain.ErrNotEditable
		}
	}
	if !stored {
		encoded, err := json.Marshal(timelineResult{Clips: filled.Clips})
		if err != nil {
			return nil, err
		}
		if _, err := s.db.ExecContext(ctx, "UPDATE versions SET timeline_json = ? WHERE id = ? AND timeline_json IS NULL", string(encoded), versionID); err != nil {
			return nil, err
		}
	}
	return src, nil
}

// plannedFrom names the steps a 方案 is planned from: the job's analysis,
// catalog and excerpt, and the plan's two steps.
func plannedFrom(plan int) []string {
	return []string{"analysis", "catalog", "song", planStep("pick", plan), planStep("shots", plan)}
}

// keptPlan is what the plan-th 方案 of a job was cut from, as the job's
// steps kept it: the analysis, catalog and song its 方案 share, and its own
// two steps of planning as answered. The analysis is read without its cards
// and their measures, which the catalog holds as they were planned from.
type keptPlan struct {
	analysis        analysisResult
	catalog         catalogResult
	excerpt         domain.Excerpt
	picked, planned json.RawMessage
}

// loadPlan reads what the plan-th 方案 of a job was cut from, for a version
// read or edited (versionSource, editSourceKey). ok is false when one of
// its steps is not done, kept nothing readable, or was done again after the
// 方案's version was saved (redoneSince).
func (s *Store) loadPlan(ctx context.Context, jobID string, plan int) (keptPlan, bool, error) {
	names := plannedFrom(plan)
	results, err := s.doneResults(ctx, jobID, names)
	if err != nil {
		return keptPlan{}, false, err
	}
	if redone, err := s.redoneSince(ctx, jobID, plan); err != nil || redone {
		return keptPlan{}, false, err
	}
	var heads struct {
		Videos []videoHead `json:"videos"`
	}
	var kept keptPlan
	if len(results) != len(names) || errors.Join(json.Unmarshal(results["analysis"], &heads),
		json.Unmarshal(results["catalog"], &kept.catalog), json.Unmarshal(results["song"], &kept.excerpt)) != nil {
		return keptPlan{}, false, nil
	}
	for _, head := range heads.Videos {
		kept.analysis.Videos = append(kept.analysis.Videos, analysedVideo{videoHead: head})
	}
	kept.picked, kept.planned = results[planStep("pick", plan)], results[planStep("shots", plan)]
	return kept, true, nil
}

// redoneSince reports whether a step the plan-th 方案 of a job was planned
// from was done again after that 方案's version was saved. The analysis,
// catalog and song are the job's, shared by its 方案: a later 方案 makes
// them again when a video is taken out or analysed again, and the cards are
// numbered anew, so the version's card and beat numbers no longer name what
// it was cut from. A job makes one 方案 now; versions made two at a time
// keep this. Times are written fixed-width (sqlitedb.FormatTime): they
// compare as text.
func (s *Store) redoneSince(ctx context.Context, jobID string, plan int) (bool, error) {
	names := plannedFrom(plan)
	args := []any{jobID, plan}
	for _, name := range names {
		args = append(args, name)
	}
	var redone bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM versions v JOIN steps s ON s.job_id = v.job_id
		WHERE v.job_id = ? AND v.plan = ? AND s.finished_at > v.created_at AND s.name IN (`+sqlitedb.Placeholders(len(names))+`))`,
		args...).Scan(&redone)
	return redone, err
}

// doneResults are what the named steps of a job kept when they were done,
// by name; a step that is not done, or that kept nothing, is not among them.
func (s *Store) doneResults(ctx context.Context, jobID string, names []string) (map[string][]byte, error) {
	args := []any{jobID}
	for _, name := range names {
		args = append(args, name)
	}
	rows, err := s.db.QueryContext(ctx, "SELECT name, result_json FROM steps WHERE job_id = ? AND state = 'done' AND result_json IS NOT NULL AND name IN ("+
		sqlitedb.Placeholders(len(names))+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := map[string][]byte{}
	for rows.Next() {
		var name, result string
		if err := rows.Scan(&name, &result); err != nil {
			return nil, err
		}
		results[name] = []byte(result)
	}
	return results, rows.Err()
}

func (src *editSource) pictureKnown(part, picture int) bool {
	parts := src.planned.Answer.Parts
	return part >= 1 && part <= len(parts) && part <= len(src.picked.Parts) && part <= len(src.music.Parts) &&
		picture >= 1 && picture <= len(parts[part-1].Pictures) && picture <= len(src.picked.Parts[part-1].Scenes)
}

// query is what the plan asked of a picture.
func (src *editSource) query(part, picture int) string {
	return src.picked.Parts[part-1].Scenes[picture-1].Query
}

// beatID names beat n, from 1, of a card: "card/n", as the fill and the
// plan write a beat (CONTEXT.md, 节拍).
func beatID(card string, n int) string { return card + "/" + strconv.Itoa(n) }

// splitBeat is the card and the number from 1 that beatID wrote as beat;
// ok is false for a text it did not write.
func splitBeat(beat string) (card string, n int, ok bool) {
	card, number, found := strings.CutLast(beat, "/")
	n, err := strconv.Atoi(number)
	if !found || err != nil || n < 1 || strconv.Itoa(n) != number {
		return "", 0, false
	}
	return card, n, true
}

// beat is a beat's card and its number from 1, and reports whether it is a
// beat of a described card. Only the catalog's cards may be cut from
// (cuttable): the fill is given no other.
func (src *editSource) beat(beat string) (videocutcompute.CatalogCard, int, bool) {
	id, n, ok := splitBeat(beat)
	card, described := src.cards[id]
	if !ok || !described || n > len(card.Times) {
		return videocutcompute.CatalogCard{}, 0, false
	}
	return card, n, true
}

// holds reports whether every shot of state leads with a beat of a card the
// version may cut from, of one of its videos. What is shown, searched,
// exported and saved of a draft reads its beats so; one whose beats the
// source no longer names cannot be edited.
func (src *editSource) holds(state draftState) bool {
	for _, shot := range state.Shots {
		card, _, ok := src.beat(shot.Beat)
		if _, video := src.videos[card.Video]; !ok || !video || !src.cuttable(card.ID) {
			return false
		}
	}
	return true
}

// cuttable reports whether a card is in the version's catalog, the cards
// the fill may cut from; a card left out (blurred, upright in a landscape
// film) is described but cannot lead a shot.
func (src *editSource) cuttable(id string) bool {
	return slices.Contains(src.catalog, id)
}

// covers reports whether a beat can fill length seconds slowed at most
// slow times: the card around its keyframe, as the fill takes it.
func (src *editSource) covers(beat string, length, slow float64) bool {
	card, n, ok := src.beat(beat)
	if !ok {
		return false
	}
	seen := card.Times[n-1]
	room := math.Min(card.End, seen+length) - math.Max(card.Start, seen-length)
	return room*slow >= length-1e-6
}

// piece is the part of a beat's video a rough preview plays for length
// seconds: from a third before the keyframe, kept within the card, as the
// fill starts a shot.
func (src *editSource) piece(beat string, length float64) (string, float64, float64) {
	card, n, _ := src.beat(beat)
	start := math.Max(card.Start, math.Min(card.Times[n-1]-length/3, card.End-length))
	return card.Video, start, math.Min(card.End, start+length)
}

// portrait reports whether a beat's video is narrower than the film.
func (src *editSource) portrait(beat string) bool {
	card, _, ok := src.beat(beat)
	if !ok {
		return false
	}
	video := src.videos[card.Video]
	return narrow(video.Width, video.Height)
}

// span is the part of its card a beat covers: from its keyframe (the
// first from the card's start) to the next beat's (the last to the card's
// end).
func (src *editSource) span(beat string) (string, float64, float64) {
	card, n, _ := src.beat(beat)
	start, end := card.Start, card.End
	if n > 1 {
		start = card.Times[n-1]
	}
	if n < len(card.Times) {
		end = card.Times[n]
	}
	return card.Video, start, end
}

// --- the draft ---

// draftSlot is a slot of the version: where it is in the film, the plan's
// part and picture it belongs to, and the beat that led it.
type draftSlot struct {
	Part    int     `json:"part"`
	Picture int     `json:"picture"`
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Beat    string  `json:"beat"`
}

// draftShot is a shot of the draft: a beat over slots First to Last. A
// shot over more than one slot took a deleted shot's slots; Home is the
// slot that was its own, whose beat the version had there.
type draftShot struct {
	Beat   string `json:"beat"`
	First  int    `json:"first"`
	Last   int    `json:"last"`
	Home   int    `json:"home"`
	Locked bool   `json:"locked,omitempty"`
}

// draftState is a revision of a draft: the version's slots, and the shots
// that cover them in film order. Only the shots are kept (encode): the
// slots are the version's, read from its timeline (slots).
type draftState struct {
	Slots []draftSlot
	Shots []draftShot
}

// keptShots is a revision as it is kept.
type keptShots struct {
	Shots []draftShot `json:"shots"`
}

// encode is the state as a revision keeps it.
func (state draftState) encode() (string, error) {
	encoded, err := json.Marshal(keptShots{Shots: state.Shots})
	return string(encoded), err
}

// stateOf is a kept revision over the version's slots; ok is false when its
// shots do not cover them.
func (src *editSource) stateOf(kept string) (draftState, bool) {
	var shots keptShots
	if err := json.Unmarshal([]byte(kept), &shots); err != nil {
		return draftState{}, false
	}
	state := draftState{Slots: src.slots(), Shots: shots.Shots}
	return state, state.valid()
}

// slots are the version's slots in film order, each with the beat that
// leads it: the first clip of a slot, but the last of the film's last slot,
// where the ending picture's beat closes the film (fill.py).
func (src *editSource) slots() []draftSlot {
	var slots []draftSlot
	var pieces [][]timelineClip
	for _, clip := range src.clips {
		if n := len(slots); n > 0 && slots[n-1].Start == clip.Slot[0] && slots[n-1].End == clip.Slot[1] {
			pieces[n-1] = append(pieces[n-1], clip)
			continue
		}
		slots = append(slots, draftSlot{Part: clip.Part, Picture: clip.Picture, Start: clip.Slot[0], End: clip.Slot[1], Beat: clip.Beat})
		pieces = append(pieces, []timelineClip{clip})
	}
	last := len(slots) - 1
	parts := src.planned.Answer.Parts
	if slot := slots[last]; slot.Part == len(parts) && slot.Picture == len(parts[slot.Part-1].Pictures) {
		slots[last].Beat = pieces[last][len(pieces[last])-1].Beat
	}
	return slots
}

// newDraftState is a version as it was made: a shot a slot.
func (src *editSource) newDraftState() draftState {
	state := draftState{Slots: src.slots()}
	for i, slot := range state.Slots {
		state.Shots = append(state.Shots, draftShot{Beat: slot.Beat, First: i, Last: i, Home: i})
	}
	return state
}

// valid checks a stored state still fits its version.
func (state draftState) valid() bool {
	next := 0
	for _, shot := range state.Shots {
		if shot.First != next || shot.Last < shot.First || shot.Last >= len(state.Slots) {
			return false
		}
		next = shot.Last + 1
	}
	return next == len(state.Slots) && next > 0
}

func (state draftState) clone() draftState {
	return draftState{Slots: state.Slots, Shots: slices.Clone(state.Shots)}
}

func (state draftState) length(shot draftShot) float64 {
	return state.Slots[shot.Last].End - state.Slots[shot.First].Start
}

// usedBy is the number, from 1, of the shot whose beat is beat; 0 none.
func (state draftState) usedBy(beat string) int {
	for i, shot := range state.Shots {
		if shot.Beat == beat {
			return i + 1
		}
	}
	return 0
}

// original is the beat the version had where the shot is: its own slot's,
// not that of a deleted shot whose slots it took.
func (state draftState) original(shot draftShot) string {
	home := shot.First
	if shot.Home > shot.First && shot.Home <= shot.Last {
		home = shot.Home
	}
	return state.Slots[home].Beat
}

// changed reports whether the shot's beat is not the one the version had
// there.
func (state draftState) changed(shot draftShot) bool {
	return state.original(shot) != shot.Beat
}

// view turns a draft's state into what is answered.
func (src *editSource) view(id string, revision int, canUndo bool, state draftState, updated time.Time) domain.Draft {
	draft := domain.Draft{ID: id, VersionID: src.versionID, ProjectID: src.projectID, VersionNumber: src.number, Revision: revision,
		CanUndo: canUndo, SongStart: src.excerpt.Start, SongTranscode: src.songTranscode, Seconds: state.Slots[len(state.Slots)-1].End, UpdatedAt: updated,
		Plan:  domain.DraftPlan{Design: src.picked.Design, Opening: src.picked.Opening, Ending: src.picked.Ending},
		Parts: []domain.DraftPart{}, Shots: []domain.DraftShot{}}
	for n, part := range src.music.Parts {
		about := domain.DraftPart{Number: n + 1, Level: part.Level, Label: part.Label, Start: part.Start, End: part.End}
		if n < len(src.picked.Parts) {
			about.Name, about.Summary = src.picked.Parts[n].Name, src.picked.Parts[n].Summary
		}
		draft.Parts = append(draft.Parts, about)
	}
	videos, portrait := map[string]bool{}, 0
	for i, shot := range state.Shots {
		first := state.Slots[shot.First]
		card, n, _ := src.beat(shot.Beat)
		about := src.about[card.ID].Beats[n-1]
		view := domain.DraftShot{Number: i + 1, Beat: shot.Beat, AssetID: card.Video, Part: first.Part, Picture: first.Picture,
			Start: first.Start, End: state.Slots[shot.Last].End, Query: src.query(first.Part, first.Picture),
			ShotSize: about.ShotSize, What: about.What, Locked: shot.Locked, Changed: state.changed(shot),
			Extended: shot.Last > shot.First, Portrait: src.portrait(shot.Beat)}
		for slot := shot.First; slot <= shot.Last; slot++ {
			view.Slots = append(view.Slots, slot+1)
		}
		view.From, view.To = src.previewPiece(shot, state)
		if view.Portrait && !videos[card.Video] {
			portrait++
		}
		videos[card.Video] = true
		draft.Shots = append(draft.Shots, view)
	}
	draft.Counts = domain.DraftCounts{Shots: len(state.Shots), Videos: len(videos), Portrait: portrait}
	return draft
}

// VersionOutline reads owner's version as its film is. It makes no draft,
// and an edit of the version not saved yet does not show in it.
func (s *Store) VersionOutline(ctx context.Context, owner, versionID string) (domain.VersionOutline, error) {
	src, err := s.versionSource(ctx, owner, versionID)
	if err != nil {
		return domain.VersionOutline{}, err
	}
	film := src.view("", 0, false, src.newDraftState(), time.Time{})
	return domain.VersionOutline{VersionID: film.VersionID, ProjectID: film.ProjectID, VersionNumber: film.VersionNumber, Seconds: film.Seconds,
		Plan: film.Plan, Parts: film.Parts, Shots: film.Shots, Counts: film.Counts, Unmet: src.unmet()}, nil
}

// unmet is what the version's plan said the videos do not have
// (docs/specs/montage-center.md, 成片存进相册): the outline's and the
// shots', each once, in order. An answer that is not a list of texts
// says nothing.
func (src *editSource) unmet() []string {
	unmet := []string{}
	for _, raw := range []json.RawMessage{src.picked.Unmet, src.planned.Answer.Unmet} {
		var texts []string
		if len(raw) == 0 || json.Unmarshal(raw, &texts) != nil {
			continue
		}
		for _, text := range texts {
			if text = strings.TrimSpace(text); text != "" && !slices.Contains(unmet, text) {
				unmet = append(unmet, text)
			}
		}
	}
	return unmet
}

// previewPiece is the part of a shot's video the rough preview plays: where
// the version's shot began for a beat the version had there, played at its
// own speed, or the beat's piece of the shot's length. Either stays within
// the beat's card: a shot longer than what is left of it (slowed in the
// version, or taking a deleted shot's slots) plays to the card's end, not on
// into the footage after it or past the video's end.
func (src *editSource) previewPiece(shot draftShot, state draftState) (float64, float64) {
	length := state.length(shot)
	start, end := state.Slots[shot.First].Start, state.Slots[shot.Last].End
	for _, clip := range src.clips {
		if clip.Beat == shot.Beat && clip.Slot[0] >= start-1e-6 && clip.Slot[1] <= end+1e-6 {
			card, _, _ := src.beat(shot.Beat)
			to := math.Min(clip.Start+length, card.End)
			if duration := src.videos[card.Video].Duration; duration > 0 {
				to = math.Min(to, duration)
			}
			return clip.Start, to
		}
	}
	_, from, to := src.piece(shot.Beat, length)
	return from, to
}

// --- reading and writing drafts ---

// VersionDraft is owner's draft of a version, made the first time.
func (s *Store) VersionDraft(ctx context.Context, owner, versionID string) (domain.Draft, error) {
	src, err := s.versionSource(ctx, owner, versionID)
	if err != nil {
		return domain.Draft{}, err
	}
	if err := s.checkAnalysis(ctx, src); err != nil {
		return domain.Draft{}, err
	}
	id, err := s.db.DraftID(ctx, owner, versionID)
	if errors.Is(err, sql.ErrNoRows) {
		encoded, err := src.newDraftState().encode()
		if err != nil {
			return domain.Draft{}, err
		}
		id = newID("draft")
		now := s.now()
		err = s.db.Update(ctx, func(tx *db.Tx) error {
			return tx.CreateDraft(ctx, id, owner, src.projectID, versionID, encoded, now)
		})
		if err != nil {
			return domain.Draft{}, err
		}
		if id, err = s.db.DraftID(ctx, owner, versionID); err != nil {
			return domain.Draft{}, err
		}
	} else if err != nil {
		return domain.Draft{}, err
	}
	return s.readDraft(ctx, owner, id, src)
}

// checkAnalysis answers ErrAnalysisChanged when the photo library's windows
// of a video the version's cards come from are not those the version was
// cut with: another model or dimensions. Windows not done now are being
// made, not yet another model: the edit job waits for them (fillRows). A
// video the library can no longer read, or holds as other content, is left
// to the steps that use it.
func (s *Store) checkAnalysis(ctx context.Context, src *editSource) error {
	checked := map[string]bool{}
	for _, id := range src.catalog {
		video := src.cards[id].Video
		was, ok := src.videos[video]
		if checked[video] || !ok {
			continue
		}
		checked[video] = true
		now, _, err := s.grants.Analysis(ctx, src.grantID, video)
		if assetGone(err) {
			continue
		}
		if err != nil {
			return domain.GrantError(err)
		}
		if was.windowsChanged(now) {
			return domain.ErrAnalysisChanged
		}
	}
	return nil
}

// keptDraft is a draft as it is kept: the state it stands at, with its
// revision and when that was written.
type keptDraft struct {
	revision int
	state    draftState
	updated  time.Time
}

// openDraft reads owner's draft as it stands and what its version was cut
// from, which src is when known.
func (s *Store) openDraft(ctx context.Context, owner, draftID string, src *editSource) (*editSource, keptDraft, error) {
	var draft keptDraft
	row, err := s.db.Draft(ctx, owner, draftID)
	versionID, stateJSON, updated := row.VersionID, row.Shots, row.Updated
	draft.revision = row.Revision
	if errors.Is(err, sql.ErrNoRows) {
		return nil, keptDraft{}, domain.ErrNotFound
	}
	if err != nil {
		return nil, keptDraft{}, err
	}
	if src == nil {
		if src, err = s.versionSource(ctx, owner, versionID); err != nil {
			return nil, keptDraft{}, err
		}
	}
	var ok bool
	if draft.state, ok = src.stateOf(stateJSON); !ok {
		return nil, keptDraft{}, fmt.Errorf("draft %s holds an unusable state", draftID)
	}
	if !src.holds(draft.state) {
		return nil, keptDraft{}, domain.ErrNotEditable
	}
	draft.updated, _ = time.Parse(time.RFC3339Nano, updated)
	return src, draft, nil
}

// readDraft answers owner's draft as it stands, with its source when known.
func (s *Store) readDraft(ctx context.Context, owner, draftID string, src *editSource) (domain.Draft, error) {
	src, draft, err := s.openDraft(ctx, owner, draftID, src)
	if err != nil {
		return domain.Draft{}, err
	}
	canUndo, err := s.canUndo(ctx, draftID, draft.revision)
	if err != nil {
		return domain.Draft{}, err
	}
	return src.view(draftID, draft.revision, canUndo, draft.state, draft.updated), nil
}

// undone is the revision an undo goes back to: its shots as kept, and the
// revision undoing goes on to from there.
type undone struct {
	revision int
	shots    string
	back     sql.NullInt64
}

// undoTarget is the revision undoing revision goes back to; ok is false
// when there is none, or it is more than keptRevisions steps back and no
// longer kept.
func (s *Store) undoTarget(ctx context.Context, draftID string, revision int) (target undone, ok bool, err error) {
	kept, err := s.db.UndoTarget(ctx, draftID, revision)
	target = undone{revision: kept.Revision, shots: kept.Shots, back: kept.Back}
	if errors.Is(err, sql.ErrNoRows) {
		return undone{}, false, nil
	}
	return target, err == nil, err
}

func (s *Store) canUndo(ctx context.Context, draftID string, revision int) (bool, error) {
	_, ok, err := s.undoTarget(ctx, draftID, revision)
	return ok, err
}

// EditDraft applies a step to owner's draft made on revision base. A draft
// that moved on answers ErrDraftConflict, a step the rules refuse an
// *EditRefusal; either way the draft stays as it was.
func (s *Store) EditDraft(ctx context.Context, owner, draftID string, base int, op domain.DraftOp) (domain.Draft, error) {
	src, draft, err := s.openDraft(ctx, owner, draftID, nil)
	if err != nil {
		return domain.Draft{}, err
	}
	if draft.revision != base {
		return domain.Draft{}, domain.ErrDraftConflict
	}
	state := draft.state
	var next draftState
	var message string
	var seed sql.NullInt64
	back := sql.NullInt64{Int64: int64(base), Valid: true}
	switch op.Type {
	case "undo":
		target, ok, err := s.undoTarget(ctx, draftID, base)
		if err != nil {
			return domain.Draft{}, err
		}
		if !ok {
			return domain.Draft{}, refuse("没有可以撤销的步骤")
		}
		if next, ok = src.stateOf(target.shots); !ok {
			return domain.Draft{}, fmt.Errorf("draft %s revision %d holds an unusable state", draftID, target.revision)
		}
		// Undoing goes on back from where the step undone came from.
		back, message = target.back, "已撤销"
	case "shuffle":
		var value int64
		if op.Seed != nil {
			value = *op.Seed
		} else {
			value = randomSeed()
		}
		seed = sql.NullInt64{Int64: value, Valid: true}
		if next, message, err = s.shuffle(ctx, src, state, value); err != nil {
			return domain.Draft{}, err
		}
	case "delete":
		if next, message, err = s.deleteShot(ctx, src, state, op.Shot); err != nil {
			return domain.Draft{}, err
		}
	default:
		if next, message, err = src.apply(state, op); err != nil {
			return domain.Draft{}, err
		}
	}
	// The shots must still cover every slot end to end: a step that broke
	// that would leave a draft no read accepts.
	if !next.valid() {
		return domain.Draft{}, fmt.Errorf("draft %s: a %s step left shots that do not cover the slots", draftID, op.Type)
	}
	encoded, err := next.encode()
	if err != nil {
		return domain.Draft{}, err
	}
	now := s.now()
	err = s.db.Update(ctx, func(tx *db.Tx) error {
		advanced, err := tx.AdvanceDraft(ctx, owner, draftID, base, encoded, now)
		if err != nil {
			return err
		}
		if !advanced {
			return domain.ErrDraftConflict
		}
		if err := tx.AddRevision(ctx, draftID, base+1, op.Type, seed, back, encoded, now); err != nil {
			return err
		}
		return trimRevisions(ctx, tx, draftID, base+1)
	})
	if err != nil {
		return domain.Draft{}, err
	}
	// Not back.Valid: an undo's back may be a revision trimmed long since,
	// past the last keptRevisions steps.
	canUndo, err := s.canUndo(ctx, draftID, base+1)
	if err != nil {
		return domain.Draft{}, err
	}
	view := src.view(draftID, base+1, canUndo, next, now)
	view.Message = message
	return view, nil
}

// trimRevisions keeps the last keptRevisions revisions and those undoing
// from latest goes back through, so that the last keptRevisions steps can
// be undone however many of them were undos themselves.
func trimRevisions(ctx context.Context, tx *db.Tx, draftID string, latest int) error {
	backs, err := tx.RevisionBacks(ctx, draftID)
	if err != nil {
		return err
	}
	keep := map[int]bool{latest: true}
	for at, n := latest, 0; n < keptRevisions; n++ {
		back := backs[at]
		if !back.Valid {
			break
		}
		at = int(back.Int64)
		keep[at] = true
	}
	for revision := range backs {
		if revision > latest-keptRevisions || keep[revision] {
			continue
		}
		if err := tx.DeleteRevision(ctx, draftID, revision); err != nil {
			return err
		}
	}
	return nil
}
