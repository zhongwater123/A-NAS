package montage

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

	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// Editing a film after it was made (#596, ADR 0039; docs/specs/montage-center.md,
// 出片后的二次编辑). A member edits a version slot by slot in a draft kept
// here: each step is checked against the rules, written as a revision, and
// can be undone. Nothing here calls a model.

// Draft errors.
var (
	// ErrDraftConflict: the draft moved on since the revision the step was
	// made on, as when another window changed it.
	ErrDraftConflict = errors.New("the draft changed since")
	// ErrNotEditable: the version's timeline is not known; it can only be
	// played.
	ErrNotEditable = errors.New("the version cannot be edited")
	// ErrAnalysisChanged: the photo library analysed the version's videos
	// again with another model since the version was cut. The version can
	// only be played, or recut (docs/specs/montage-center.md, 修改的规则).
	ErrAnalysisChanged = errors.New("the videos were analysed again with another model")
	// ErrSearchUnavailable: the photo library cannot search windows now.
	ErrSearchUnavailable = errors.New("searching the videos is not available")
	// ErrSearchModel: the text model is not the one the windows were
	// analysed with; they are analysed again first.
	ErrSearchModel = errors.New("the windows were analysed with another model")
)

// AnalysisChangedReason tells the member why a version analysed again can
// no longer be edited, on the edit page and on a save that found it.
const AnalysisChangedReason = "素材已按新模型重新分析，这一版不能再修改，可以用一句话重剪"

// EditRefusal is a step the rules do not allow; Reason says why, for the
// member.
type EditRefusal struct{ Reason string }

func (r *EditRefusal) Error() string { return "the edit is refused: " + r.Reason }

func refuse(format string, args ...any) error {
	return &EditRefusal{Reason: fmt.Sprintf(format, args...)}
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

// timelineClip is one shot of a version's timeline as the fill wrote it.
type timelineClip struct {
	Card     string     `json:"card"`
	Beats    []int      `json:"beats"`
	Beat     string     `json:"beat"`
	Video    string     `json:"video"`
	Start    float64    `json:"start"`
	End      float64    `json:"end"`
	Slot     [2]float64 `json:"slot"`
	Share    float64    `json:"share"`
	Slow     float64    `json:"slow,omitempty"`
	Part     int        `json:"part"`
	PartName string     `json:"partName"`
	Picture  int        `json:"picture"`
	Level    string     `json:"level"`
	Query    string     `json:"query"`
	ShotSize string     `json:"shotSize"`
	What     string     `json:"what"`
}

// plannedPicture and pickedScene are a picture of the plan's two steps:
// the beats the second step listed, and the first step's description and
// cards.
type plannedPicture struct {
	Beats []string `json:"beats"`
	Shows string   `json:"shows"`
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
			Query string   `json:"query"`
			Cards []string `json:"cards"`
		} `json:"scenes"`
	} `json:"parts"`
}

type musicDoc struct {
	Seconds float64 `json:"seconds"`
	Parts   []struct {
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
	versionID, projectID, owner, grantID, jobID string
	number, plan                                int
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
	cards                 map[string]Card
	about                 map[string]cardAbout
	catalog               []string
	videos                map[string]analysedVideo
	planned               plannedDoc
	picked                pickedDoc
	music                 musicDoc
	excerpt               Excerpt
}

// versionSource reads what owner's version was cut from. A version saved
// before versions kept their timeline takes its job's fill, and keeps it.
func (s *Store) versionSource(ctx context.Context, owner, versionID string) (*editSource, error) {
	src := &editSource{versionID: versionID, owner: owner}
	var timeline sql.NullString
	var grantID sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT v.project_id, v.number, v.job_id, v.plan, v.timeline_json, p.grant_id
		FROM versions v JOIN projects p ON p.id = v.project_id WHERE v.id = ? AND p.owner_user_id = ? AND p.state = 'ready'`,
		versionID, owner).Scan(&src.projectID, &src.number, &src.jobID, &src.plan, &timeline, &grantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	src.grantID = grantID.String
	src.planJob, src.planNo = src.jobID, src.plan
	var settingsJSON string
	if err := s.db.QueryRowContext(ctx, "SELECT settings_json FROM jobs WHERE id = ?", src.jobID).Scan(&settingsJSON); err != nil {
		return nil, err
	}
	var settings JobSettings
	if err := json.Unmarshal([]byte(settingsJSON), &settings); err == nil && settings.Edit != nil {
		src.planJob, src.planNo = settings.Edit.Job, settings.Edit.Plan
	}
	if settings.SongID != "" {
		// A song gone since is not heard at all (OpenDraftSongAudio).
		if song, err := s.song(ctx, s.db, settings.SongID); err == nil {
			src.songTranscode = song.contentType == ""
		}
	}
	results := map[string][]byte{}
	for _, name := range []string{"analysis", "catalog", "song", planStep("pick", src.planNo), planStep("shots", src.planNo), planStep("fill", src.plan)} {
		job := src.planJob
		if name == planStep("fill", src.plan) {
			job = src.jobID
		}
		result, kept, err := s.doneResult(ctx, job, name)
		if err != nil {
			return nil, err
		}
		if kept {
			results[name] = result
		}
	}
	stored := timeline.Valid
	if !stored {
		timeline.String = string(results[planStep("fill", src.plan)])
	}
	var filled timelineResult
	var analysis analysisResult
	var catalog catalogResult
	if timeline.String == "" || json.Unmarshal([]byte(timeline.String), &filled) != nil || len(filled.Clips) == 0 ||
		json.Unmarshal(results["analysis"], &analysis) != nil || json.Unmarshal(results["catalog"], &catalog) != nil ||
		json.Unmarshal(results["song"], &src.excerpt) != nil || json.Unmarshal(src.excerpt.Music, &src.music) != nil ||
		json.Unmarshal(results[planStep("pick", src.planNo)], &src.picked) != nil ||
		json.Unmarshal(results[planStep("shots", src.planNo)], &src.planned) != nil {
		return nil, ErrNotEditable
	}
	src.pickedRaw, src.plannedRaw = results[planStep("pick", src.planNo)], results[planStep("shots", src.planNo)]
	for _, raw := range filled.Clips {
		var clip timelineClip
		if err := json.Unmarshal(raw, &clip); err != nil {
			return nil, ErrNotEditable
		}
		src.clips = append(src.clips, clip)
	}
	src.cards, src.catalog = catalog.Cards, catalog.Catalog
	src.about, src.videos = map[string]cardAbout{}, map[string]analysedVideo{}
	for id, card := range src.cards {
		var about cardAbout
		if json.Unmarshal(card.Answer, &about) != nil || len(about.Beats) != len(card.Times) {
			return nil, ErrNotEditable
		}
		src.about[id] = about
	}
	for _, video := range analysis.Videos {
		video.Cards = nil
		src.videos[video.AssetID] = video
	}
	for _, clip := range src.clips {
		// Each shot is shown by its beat's description (view): a beat its
		// card does not have cannot be.
		if _, _, known := src.beat(clip.Beat); !known || !src.pictureKnown(clip.Part, clip.Picture) {
			return nil, ErrNotEditable
		}
		if _, ok := src.cards[clip.Card]; !ok {
			return nil, ErrNotEditable
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

// doneResult is what a job's step kept when it was done; kept is false for
// a step that is not done, or that kept nothing.
func (s *Store) doneResult(ctx context.Context, jobID, name string) (result []byte, kept bool, err error) {
	var stored sql.NullString
	err = s.db.QueryRowContext(ctx, "SELECT result_json FROM steps WHERE job_id = ? AND name = ? AND state = 'done'", jobID, name).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return []byte(stored.String), stored.Valid, err
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
func (src *editSource) beat(beat string) (Card, int, bool) {
	id, n, ok := splitBeat(beat)
	card, described := src.cards[id]
	if !ok || !described || n > len(card.Times) {
		return Card{}, 0, false
	}
	return card, n, true
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
// that cover them in film order.
type draftState struct {
	Slots []draftSlot `json:"slots"`
	Shots []draftShot `json:"shots"`
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

// Draft is a member's edit of a version, as the page shows it.
type Draft struct {
	ID            string `json:"id"`
	VersionID     string `json:"versionId"`
	ProjectID     string `json:"projectId"`
	VersionNumber int    `json:"versionNumber"`
	// Revision is the draft's revision, which a step is made on; CanUndo
	// whether the step before it can be gone back to.
	Revision int  `json:"revision"`
	CanUndo  bool `json:"canUndo"`
	// SongStart is where the version's excerpt starts in its song, and
	// Seconds how long the film is. SongTranscode: the song is decoded on
	// the way and cannot seek, so the rough preview asks for it from
	// where it plays.
	SongStart     float64     `json:"songStart"`
	SongTranscode bool        `json:"songTranscode"`
	Seconds       float64     `json:"seconds"`
	Plan          DraftPlan   `json:"plan"`
	Parts         []DraftPart `json:"parts"`
	Shots         []DraftShot `json:"shots"`
	Message       string      `json:"message,omitempty"`
	Counts        DraftCounts `json:"counts"`
	UpdatedAt     time.Time   `json:"updatedAt"`
	source        *editSource
	state         draftState
}

// DraftPlan is the plan the version was cut by: the first step's design,
// opening and ending.
type DraftPlan struct {
	Design  string `json:"design"`
	Opening string `json:"opening"`
	Ending  string `json:"ending"`
}

// DraftPart is a part of the music and of the plan.
type DraftPart struct {
	Number  int    `json:"number"`
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Level   string `json:"level"`
	// Label is the music's section (allin1's: intro, verse, chorus, build…);
	// a build leads into a chorus (铺垫).
	Label string  `json:"label"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// DraftCounts are the draft's shots, the videos they come from, and how
// many of those are 竖.
type DraftCounts struct {
	Shots    int `json:"shots"`
	Videos   int `json:"videos"`
	Portrait int `json:"portrait"`
}

// DraftShot is a shot of the draft. Start and End are its place in the
// film; From and To the part of its video a rough preview plays.
type DraftShot struct {
	Number   int     `json:"number"`
	Beat     string  `json:"beat"`
	AssetID  string  `json:"assetId"`
	Part     int     `json:"part"`
	Picture  int     `json:"picture"`
	Slots    []int   `json:"slots"`
	Start    float64 `json:"start"`
	End      float64 `json:"end"`
	From     float64 `json:"from"`
	To       float64 `json:"to"`
	Query    string  `json:"query"`
	ShotSize string  `json:"shotSize"`
	What     string  `json:"what"`
	Locked   bool    `json:"locked"`
	Changed  bool    `json:"changed"`
	Extended bool    `json:"extended"`
	Portrait bool    `json:"portrait"`
}

// view turns a draft's state into what the page shows.
func (src *editSource) view(id string, revision int, canUndo bool, state draftState, updated time.Time) Draft {
	draft := Draft{ID: id, VersionID: src.versionID, ProjectID: src.projectID, VersionNumber: src.number, Revision: revision,
		CanUndo: canUndo, SongStart: src.excerpt.Start, SongTranscode: src.songTranscode, Seconds: state.Slots[len(state.Slots)-1].End, UpdatedAt: updated,
		Plan:  DraftPlan{Design: src.picked.Design, Opening: src.picked.Opening, Ending: src.picked.Ending},
		Parts: []DraftPart{}, Shots: []DraftShot{}, source: src, state: state}
	for n, part := range src.music.Parts {
		about := DraftPart{Number: n + 1, Level: part.Level, Label: part.Label, Start: part.Start, End: part.End}
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
		view := DraftShot{Number: i + 1, Beat: shot.Beat, AssetID: card.Video, Part: first.Part, Picture: first.Picture,
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
	draft.Counts = DraftCounts{Shots: len(state.Shots), Videos: len(videos), Portrait: portrait}
	return draft
}

// VersionOutline is a version as its film is: the plan it was cut by and
// its shots, read without making a draft.
type VersionOutline struct {
	VersionID     string      `json:"versionId"`
	ProjectID     string      `json:"projectId"`
	VersionNumber int         `json:"versionNumber"`
	Seconds       float64     `json:"seconds"`
	Plan          DraftPlan   `json:"plan"`
	Parts         []DraftPart `json:"parts"`
	Shots         []DraftShot `json:"shots"`
	Counts        DraftCounts `json:"counts"`
	// Unmet is what the plan found the videos do not have.
	Unmet []string `json:"unmet"`
}

// VersionOutline reads owner's version as its film is. It makes no draft,
// and an edit of the version not saved yet does not show in it.
func (s *Store) VersionOutline(ctx context.Context, owner, versionID string) (VersionOutline, error) {
	src, err := s.versionSource(ctx, owner, versionID)
	if err != nil {
		return VersionOutline{}, err
	}
	film := src.view("", 0, false, src.newDraftState(), time.Time{})
	return VersionOutline{VersionID: film.VersionID, ProjectID: film.ProjectID, VersionNumber: film.VersionNumber, Seconds: film.Seconds,
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
// own speed, or the beat's piece of the shot's length.
func (src *editSource) previewPiece(shot draftShot, state draftState) (float64, float64) {
	length := state.length(shot)
	start, end := state.Slots[shot.First].Start, state.Slots[shot.Last].End
	for _, clip := range src.clips {
		if clip.Beat == shot.Beat && clip.Slot[0] >= start-1e-6 && clip.Slot[1] <= end+1e-6 {
			return clip.Start, clip.Start + length
		}
	}
	_, from, to := src.piece(shot.Beat, length)
	return from, to
}

// --- reading and writing drafts ---

// VersionDraft is owner's draft of a version, made the first time.
func (s *Store) VersionDraft(ctx context.Context, owner, versionID string) (Draft, error) {
	src, err := s.versionSource(ctx, owner, versionID)
	if err != nil {
		return Draft{}, err
	}
	if err := s.checkAnalysis(ctx, src); err != nil {
		return Draft{}, err
	}
	var id string
	err = s.db.QueryRowContext(ctx, "SELECT id FROM drafts WHERE base_version_id = ? AND owner_user_id = ?", versionID, owner).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		state := src.newDraftState()
		encoded, err := json.Marshal(state)
		if err != nil {
			return Draft{}, err
		}
		id = newID("draft")
		now := sqlitedb.FormatTime(s.now())
		err = s.withTx(ctx, func(tx *sql.Tx) error {
			result, err := tx.ExecContext(ctx, `INSERT INTO drafts(id, owner_user_id, project_id, base_version_id, revision, slots_json, updated_at)
				VALUES(?, ?, ?, ?, 0, ?, ?) ON CONFLICT (base_version_id) DO NOTHING`, id, owner, src.projectID, versionID, string(encoded), now)
			if err != nil {
				return err
			}
			if n, err := result.RowsAffected(); err != nil || n == 0 {
				return err // another request made it first
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO draft_revisions(draft_id, revision, op, slots_json, created_at) VALUES(?, 0, 'create', ?, ?)`,
				id, string(encoded), now)
			return err
		})
		if err != nil {
			return Draft{}, err
		}
		if err := s.db.QueryRowContext(ctx, "SELECT id FROM drafts WHERE base_version_id = ? AND owner_user_id = ?", versionID, owner).Scan(&id); err != nil {
			return Draft{}, err
		}
	} else if err != nil {
		return Draft{}, err
	}
	return s.readDraft(ctx, owner, id, src)
}

// checkAnalysis answers ErrAnalysisChanged when the photo library's windows
// of a video the version's cards come from are not those the version was cut
// with: another model or dimensions, or none now. A video the library no
// longer has, or holds as other content, is left to the steps that use it.
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
		if code := photosapi.GrantErrorCode(err); code == photosapi.GrantAssetNotFound {
			continue
		}
		if err != nil {
			return grantError(err)
		}
		if now.ObjectID == was.ObjectID && (now.Identity != was.Identity || now.Dimensions != was.Dimensions) {
			return ErrAnalysisChanged
		}
	}
	return nil
}

// readDraft reads owner's draft as it stands, with its source when known.
func (s *Store) readDraft(ctx context.Context, owner, draftID string, src *editSource) (Draft, error) {
	var versionID, stateJSON, updated string
	var revision int
	err := s.db.QueryRowContext(ctx, "SELECT base_version_id, revision, slots_json, updated_at FROM drafts WHERE id = ? AND owner_user_id = ?",
		draftID, owner).Scan(&versionID, &revision, &stateJSON, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Draft{}, ErrNotFound
	}
	if err != nil {
		return Draft{}, err
	}
	if src == nil {
		if src, err = s.versionSource(ctx, owner, versionID); err != nil {
			return Draft{}, err
		}
	}
	var state draftState
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil || !state.valid() {
		return Draft{}, fmt.Errorf("draft %s holds an unusable state", draftID)
	}
	canUndo, err := s.canUndo(ctx, draftID, revision)
	if err != nil {
		return Draft{}, err
	}
	at, _ := time.Parse(time.RFC3339Nano, updated)
	return src.view(draftID, revision, canUndo, state, at), nil
}

// undoTarget is the revision undoing revision goes back to, if it is still
// kept.
func (s *Store) undoTarget(ctx context.Context, q queryer, draftID string, revision int) (int, string, bool, error) {
	var back sql.NullInt64
	if err := q.QueryRowContext(ctx, "SELECT back FROM draft_revisions WHERE draft_id = ? AND revision = ?", draftID, revision).Scan(&back); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, "", false, nil
		}
		return 0, "", false, err
	}
	if !back.Valid {
		return 0, "", false, nil
	}
	var stateJSON string
	err := q.QueryRowContext(ctx, "SELECT slots_json FROM draft_revisions WHERE draft_id = ? AND revision = ?", draftID, back.Int64).Scan(&stateJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, nil // more than keptRevisions steps back
	}
	if err != nil {
		return 0, "", false, err
	}
	return int(back.Int64), stateJSON, true, nil
}

func (s *Store) canUndo(ctx context.Context, draftID string, revision int) (bool, error) {
	_, _, ok, err := s.undoTarget(ctx, s.db, draftID, revision)
	return ok, err
}

// DraftOp is one step of an edit. Shot and To are shot numbers from 1;
// Seed, for a shuffle, makes it again as it was.
type DraftOp struct {
	Type string `json:"type"`
	Shot int    `json:"shot,omitempty"`
	To   int    `json:"to,omitempty"`
	Beat string `json:"beat,omitempty"`
	Seed *int64 `json:"seed,omitempty"`
}

// EditDraft applies a step to owner's draft made on revision base. A draft
// that moved on answers ErrDraftConflict, a step the rules refuse an
// *EditRefusal; either way the draft stays as it was.
func (s *Store) EditDraft(ctx context.Context, owner, draftID string, base int, op DraftOp) (Draft, error) {
	draft, err := s.readDraft(ctx, owner, draftID, nil)
	if err != nil {
		return Draft{}, err
	}
	if draft.Revision != base {
		return Draft{}, ErrDraftConflict
	}
	src, state := draft.source, draft.state
	var next draftState
	var message string
	var seed sql.NullInt64
	back := sql.NullInt64{Int64: int64(base), Valid: true}
	switch op.Type {
	case "undo":
		target, stateJSON, ok, err := s.undoTarget(ctx, s.db, draftID, base)
		if err != nil {
			return Draft{}, err
		}
		if !ok {
			return Draft{}, refuse("没有可以撤销的步骤")
		}
		if err := json.Unmarshal([]byte(stateJSON), &next); err != nil || !next.valid() {
			return Draft{}, fmt.Errorf("draft %s revision %d holds an unusable state", draftID, target)
		}
		// Undoing goes on back from where the step undone came from.
		var further sql.NullInt64
		if err := s.db.QueryRowContext(ctx, "SELECT back FROM draft_revisions WHERE draft_id = ? AND revision = ?", draftID, target).Scan(&further); err != nil {
			return Draft{}, err
		}
		back, message = further, "已撤销"
	case "shuffle":
		var value int64
		if op.Seed != nil {
			value = *op.Seed
		} else {
			value = randomSeed()
		}
		seed = sql.NullInt64{Int64: value, Valid: true}
		if next, message, err = s.shuffle(ctx, src, state, value); err != nil {
			return Draft{}, err
		}
	case "delete":
		if next, message, err = s.deleteShot(ctx, src, state, op.Shot); err != nil {
			return Draft{}, err
		}
	default:
		if next, message, err = src.apply(state, op); err != nil {
			return Draft{}, err
		}
	}
	// The shots must still cover every slot end to end: a step that broke
	// that would leave a draft no read accepts.
	if !next.valid() {
		return Draft{}, fmt.Errorf("draft %s: a %s step left shots that do not cover the slots", draftID, op.Type)
	}
	encoded, err := json.Marshal(next)
	if err != nil {
		return Draft{}, err
	}
	now := s.now()
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE drafts SET revision = revision + 1, slots_json = ?, updated_at = ? WHERE id = ? AND owner_user_id = ? AND revision = ?",
			string(encoded), sqlitedb.FormatTime(now), draftID, owner, base)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return ErrDraftConflict
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO draft_revisions(draft_id, revision, op, seed, back, slots_json, created_at) VALUES(?, ?, ?, ?, ?, ?, ?)`,
			draftID, base+1, op.Type, seed, back, string(encoded), sqlitedb.FormatTime(now)); err != nil {
			return err
		}
		return trimRevisions(ctx, tx, draftID, base+1)
	})
	if err != nil {
		return Draft{}, err
	}
	canUndo, err := s.canUndo(ctx, draftID, base+1)
	if err != nil {
		return Draft{}, err
	}
	view := src.view(draftID, base+1, canUndo, next, now)
	view.Message = message
	return view, nil
}

// trimRevisions keeps the last keptRevisions revisions and those undoing
// from latest goes back through, so that the last keptRevisions steps can
// be undone however many of them were undos themselves.
func trimRevisions(ctx context.Context, tx *sql.Tx, draftID string, latest int) error {
	backs, err := revisionBacks(ctx, tx, draftID)
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
		if _, err := tx.ExecContext(ctx, "DELETE FROM draft_revisions WHERE draft_id = ? AND revision = ?", draftID, revision); err != nil {
			return err
		}
	}
	return nil
}

// revisionBacks is each kept revision of a draft with the one undoing it
// goes back to.
func revisionBacks(ctx context.Context, tx *sql.Tx, draftID string) (map[int]sql.NullInt64, error) {
	rows, err := tx.QueryContext(ctx, "SELECT revision, back FROM draft_revisions WHERE draft_id = ?", draftID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	backs := map[int]sql.NullInt64{}
	for rows.Next() {
		var revision int
		var back sql.NullInt64
		if err := rows.Scan(&revision, &back); err != nil {
			return nil, err
		}
		backs[revision] = back
	}
	return backs, rows.Err()
}
