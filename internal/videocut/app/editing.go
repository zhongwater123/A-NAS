package app

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
)

// What editing a film reads besides the draft (#596): candidates for a
// shot, confirming a version, the shot list, and thumbnails.

// --- candidates ---

// CandidateQuery asks for the beats that could lead shot Shot: Q searches
// all the videos, and Size, Scene and Landscape filter what is searched or
// listed.
type CandidateQuery struct {
	Shot      int
	Q         string
	Size      string
	Scene     string
	Landscape bool
}

// Candidate is a beat that could lead the shot. From and To are the part
// of its video a try plays; Fits whether it is long enough; UsedBy the
// shot that has it, 0 none.
type Candidate struct {
	Beat     string     `json:"beat"`
	AssetID  string     `json:"assetId"`
	From     float64    `json:"from"`
	To       float64    `json:"to"`
	ShotSize string     `json:"shotSize"`
	What     string     `json:"what"`
	Scene    string     `json:"scene"`
	Portrait bool       `json:"portrait"`
	Fits     bool       `json:"fits"`
	UsedBy   int        `json:"usedBy,omitempty"`
	Score    *float64   `json:"score,omitempty"`
	TakenAt  *time.Time `json:"takenAt,omitempty"`
}

// SceneCount is a scene the cards were described in, and how many.
type SceneCount struct {
	Scene string `json:"scene"`
	Cards int    `json:"cards"`
}

// Candidates are the beats for a shot: the rest of its card, the plan's
// spare beats for its picture, and the search's results, or every beat
// in shooting order when nothing was searched for.
type Candidates struct {
	Shot     int          `json:"shot"`
	Query    string       `json:"query"`
	SameCard []Candidate  `json:"sameCard"`
	Planned  []Candidate  `json:"planned"`
	Results  []Candidate  `json:"results"`
	Scenes   []SceneCount `json:"scenes"`
}

const (
	// maxResults is how many of a search's beats are listed, best first.
	maxResults = 60
	// shownScenes is how many scenes the filter lists; the rest are 其他.
	shownScenes = 8
	otherScene  = "其他"
)

// DraftCandidates lists the beats that could lead a shot of owner's draft.
func (s *Store) DraftCandidates(ctx context.Context, owner, draftID string, query CandidateQuery) (Candidates, error) {
	// The photo library counts a search's words without the blank around
	// them (photos.QueryText); so does this.
	q := strings.TrimSpace(query.Q)
	if !validText(q, photos.MaxQueryRunes) || !validText(query.Size, MaxNameRunes) || !validText(query.Scene, MaxNameRunes) {
		return Candidates{}, domain.ErrInvalid
	}
	src, draft, err := s.openDraft(ctx, owner, draftID, nil)
	if err != nil {
		return Candidates{}, err
	}
	state := draft.state
	i, err := shotAt(state, query.Shot)
	if err != nil {
		return Candidates{}, err
	}
	shot := state.Shots[i]
	slot := state.Slots[shot.First]
	length := state.length(shot)
	scenes := src.scenes()
	answer := Candidates{Shot: query.Shot, Query: src.query(slot.Part, slot.Picture), SameCard: []Candidate{}, Planned: []Candidate{},
		Results: []Candidate{}, Scenes: scenes}
	listed := map[string]bool{shot.Beat: true}
	add := func(list *[]Candidate, beat string, score *float64) {
		if listed[beat] {
			return
		}
		if _, _, ok := src.beat(beat); !ok {
			return
		}
		listed[beat] = true
		*list = append(*list, src.candidate(state, beat, length, score))
	}
	card, _, _ := src.beat(shot.Beat)
	for n := range card.Times {
		add(&answer.SameCard, beatID(card.ID, n+1), nil)
	}
	for _, beat := range src.planned.Answer.Parts[slot.Part-1].Pictures[slot.Picture-1].Beats {
		add(&answer.Planned, beat, nil)
	}
	keep := func(candidate Candidate) bool {
		if query.Size != "" && candidate.ShotSize != query.Size {
			return false
		}
		if query.Landscape && candidate.Portrait {
			return false
		}
		switch query.Scene {
		case "":
			return true
		case otherScene:
			return !slices.ContainsFunc(scenes, func(c SceneCount) bool { return c.Scene == candidate.Scene && c.Scene != otherScene })
		}
		return candidate.Scene == query.Scene
	}
	var results []Candidate
	searching := q != ""
	if !searching {
		for _, id := range src.catalog {
			for n := range src.cards[id].Times {
				add(&results, beatID(id, n+1), nil)
			}
		}
	} else {
		found, err := s.searchBeats(ctx, src, []string{q})
		if err != nil {
			return Candidates{}, err
		}
		for _, hit := range found[0] {
			score := hit.score
			add(&results, hit.beat, &score)
		}
	}
	for _, candidate := range results {
		if keep(candidate) && (!searching || len(answer.Results) < maxResults) {
			answer.Results = append(answer.Results, candidate)
		}
	}
	return answer, nil
}

func (src *editSource) candidate(state draftState, beat string, length float64, score *float64) Candidate {
	card, n, _ := src.beat(beat)
	about := src.about[card.ID]
	video, from, to := src.piece(beat, length)
	candidate := Candidate{Beat: beat, AssetID: video, From: from, To: to, ShotSize: about.Beats[n-1].ShotSize,
		What: about.Beats[n-1].What, Scene: about.Scene, Portrait: src.portrait(beat), Fits: src.covers(beat, length, slowMost),
		UsedBy: state.usedBy(beat), Score: score}
	if taken := src.videos[video].TakenAt; taken != nil {
		at := taken.Add(time.Duration(card.Times[n-1] * float64(time.Second)))
		candidate.TakenAt = &at
	}
	return candidate
}

// scenes are the catalog's scenes by how many cards were described in
// them, the first shownScenes and the rest together as 其他.
func (src *editSource) scenes() []SceneCount {
	counts := map[string]int{}
	for _, id := range src.catalog {
		counts[src.about[id].Scene]++
	}
	all := []SceneCount{}
	for scene, cards := range counts {
		if scene != "" {
			all = append(all, SceneCount{Scene: scene, Cards: cards})
		}
	}
	slices.SortFunc(all, func(a, b SceneCount) int { return cmp.Or(cmp.Compare(b.Cards, a.Cards), cmp.Compare(a.Scene, b.Scene)) })
	if len(all) <= shownScenes {
		return all
	}
	rest := 0
	for _, scene := range all[shownScenes:] {
		rest += scene.Cards
	}
	return append(all[:shownScenes:shownScenes], SceneCount{Scene: otherScene, Cards: rest})
}

// --- confirming a version ---

// ConfirmVersion makes owner's version the project's film. No film is
// deleted; another version can be confirmed later
// (docs/decisions/2026-11-19-montage-edit-defaults.md).
func (s *Store) ConfirmVersion(ctx context.Context, owner, versionID string) (ProjectDetail, error) {
	var projectID string
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT v.project_id FROM versions v JOIN projects p ON p.id = v.project_id
			WHERE v.id = ? AND p.owner_user_id = ? AND p.state = 'ready'`, versionID, owner).Scan(&projectID)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE projects SET chosen_version_id = ?, updated_at = ? WHERE id = ?",
			versionID, sqlitedb.FormatTime(s.now()), projectID)
		return err
	})
	if err != nil {
		return ProjectDetail{}, err
	}
	return s.Project(ctx, owner, projectID)
}

// --- the shot list ---

// shotListHeader names the shot list's columns
// (docs/decisions/2026-11-19-montage-edit-defaults.md).
var shotListHeader = []string{"镜号", "音乐槽", "成片起（秒）", "成片止（秒）", "段落", "资产 ID", "拍摄时间", "原片起（秒）", "原片止（秒）", "放慢", "改过", "备注"}

// byteOrderMark lets a spreadsheet open the UTF-8 list as UTF-8.
const byteOrderMark = "\uFEFF"

func formatSeconds(value float64) string { return strconv.FormatFloat(value, 'f', 2, 64) }

func (src *editSource) takenAt(video string, at float64) string {
	taken := src.videos[video].TakenAt
	if taken == nil {
		return ""
	}
	return taken.Add(time.Duration(at * float64(time.Second))).Format(time.DateTime)
}

func (src *editSource) partName(part int) string {
	if part >= 1 && part <= len(src.picked.Parts) {
		return src.picked.Parts[part-1].Name
	}
	return ""
}

// asText keeps a spreadsheet from reading a cell as a formula: the plan's
// names and notes are a model's words. A leading apostrophe shows the text
// as it is.
func asText(cell string) string {
	if cell != "" && strings.ContainsRune("=+-@\t\r", rune(cell[0])) {
		return "'" + cell
	}
	return cell
}

func writeList(rows [][]string) ([]byte, error) {
	for _, row := range rows {
		for i, cell := range row {
			row[i] = asText(cell)
		}
	}
	var out bytes.Buffer
	out.WriteString(byteOrderMark)
	w := csv.NewWriter(&out)
	w.UseCRLF = true
	if err := w.Write(shotListHeader); err != nil {
		return nil, err
	}
	if err := w.WriteAll(rows); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// VersionShots is owner's version's shot list, a row a shot of its film.
func (s *Store) VersionShots(ctx context.Context, owner, versionID string) ([]byte, error) {
	src, err := s.versionSource(ctx, owner, versionID)
	if err != nil {
		return nil, err
	}
	slots := src.slots()
	var rows [][]string
	for n, clip := range src.clips {
		slot := slices.IndexFunc(slots, func(slot draftSlot) bool { return slot.Start == clip.Slot[0] && slot.End == clip.Slot[1] })
		slow := clip.Slow
		if slow == 0 {
			slow = 1
		}
		at := clip.Slot[0]
		for _, before := range src.clips[:n] {
			if before.Slot == clip.Slot {
				at += before.Share
			}
		}
		rows = append(rows, []string{strconv.Itoa(n + 1), strconv.Itoa(slot + 1), formatSeconds(at), formatSeconds(at + clip.Share),
			src.partName(clip.Part), clip.Video, src.takenAt(clip.Video, clip.Start), formatSeconds(clip.Start), formatSeconds(clip.End),
			strconv.FormatFloat(slow, 'f', -1, 64), "否", ""})
	}
	return writeList(rows)
}

// DraftShots is owner's draft's shot list, a row a shot. Where a shot took
// a deleted shot's slots, the note says so. Until the draft is saved as a
// version, a shot's piece of its video is where the rough preview plays.
func (s *Store) DraftShots(ctx context.Context, owner, draftID string) ([]byte, error) {
	src, draft, err := s.openDraft(ctx, owner, draftID, nil)
	if err != nil {
		return nil, err
	}
	state := draft.state
	var rows [][]string
	for n, shot := range src.view(draftID, draft.revision, false, state, draft.updated).Shots {
		own := state.Shots[n]
		slots := strconv.Itoa(own.First + 1)
		if own.Last > own.First {
			slots += "–" + strconv.Itoa(own.Last+1)
		}
		changed, slow, note := "否", "", ""
		if shot.Changed {
			changed = "是"
		} else {
			slow = "1"
			for _, clip := range src.clips {
				if clip.Beat == shot.Beat && clip.Slow > 0 {
					slow = strconv.FormatFloat(clip.Slow, 'f', -1, 64)
				}
			}
		}
		if own.Last > own.First {
			var taken []string
			for i := own.First; i <= own.Last; i++ {
				if state.Slots[i].Beat != state.original(own) {
					taken = append(taken, strconv.Itoa(i+1))
				}
			}
			note = fmt.Sprintf("第 %s 槽的镜头已删除，由这一镜延长补上", strings.Join(taken, "、"))
		}
		rows = append(rows, []string{strconv.Itoa(shot.Number), slots, formatSeconds(shot.Start), formatSeconds(shot.End), src.partName(shot.Part),
			shot.AssetID, src.takenAt(shot.AssetID, shot.From), formatSeconds(shot.From), formatSeconds(shot.To), slow, changed, note})
	}
	return writeList(rows)
}

// --- thumbnails ---

const (
	// thumbnailEdge is a thumbnail's longer side.
	thumbnailEdge = 320
	// thumbnailQuality is the JPEG's quality scale (2 best, 31 worst).
	thumbnailQuality = 5
	// MaxThumbnails is how many thumbnails are taken at once: the member
	// waits for them, so they are Interactive and outside the montage
	// center's one Background request.
	MaxThumbnails = 4
)

// Thumbnail is a frame of a video of owner's project at ms milliseconds,
// a JPEG whose longer side is thumbnailEdge. It is not kept: the content
// does not change, so the browser keeps it.
func (s *Store) Thumbnail(ctx context.Context, owner, projectID, assetID string, ms int64) ([]byte, error) {
	if ms < 0 || ms > math.MaxInt32 {
		return nil, domain.ErrInvalid
	}
	if s.editing == nil {
		return nil, domain.ErrJobsUnavailable
	}
	var grantID sql.NullString
	var object string
	err := s.db.QueryRowContext(ctx, `SELECT p.grant_id, a.object_id FROM projects p JOIN project_assets a ON a.project_id = p.id
		WHERE p.id = ? AND p.owner_user_id = ? AND p.state = 'ready' AND a.asset_id = ?`, projectID, owner, assetID).Scan(&grantID, &object)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !grantID.Valid {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	select {
	case s.thumbnails <- struct{}{}:
		defer func() { <-s.thumbnails }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	file, opened, err := s.grants.Open(ctx, grantID.String, assetID)
	if assetGone(err) {
		return nil, domain.ErrNotFound // there is no frame of it
	}
	if err != nil {
		return nil, grantError(err)
	}
	defer func() { _ = file.Close() }()
	if opened != object {
		return nil, domain.ErrNotFound // the video is not the one the project holds
	}
	var frame []byte
	request := ffmpegworker.FramesRequest{Times: []float64{float64(ms) / 1000}, Format: ffmpegworker.FormatJPEG,
		LongEdge: thumbnailEdge, Quality: thumbnailQuality}
	_, err = s.editing.Frames(ffmpegworker.WithPriority(ctx, ffmpegworker.Interactive), file, request, func(jpeg []byte) error {
		if frame != nil {
			return errors.New("more than one frame")
		}
		frame = bytes.Clone(jpeg)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", domain.ErrMediaUnavailable, err)
	}
	if frame == nil {
		return nil, fmt.Errorf("%w: no frame", domain.ErrMediaUnavailable)
	}
	return frame, nil
}
