package montage

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/bits"
	mathrand "math/rand/v2"
	"slices"
	"sort"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

// The rules of each step (docs/specs/montage-center.md, 修改的规则): the
// unit is the music's slot, whose length and cuts stay; a beat is used once
// in a film; the cards must be long enough.

func randomSeed() int64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return int64(binary.LittleEndian.Uint64(b[:]) >> 1)
}

// shotAt is shot number n of state, from 1.
func shotAt(state draftState, n int) (int, error) {
	if n < 1 || n > len(state.Shots) {
		return 0, ErrInvalid
	}
	return n - 1, nil
}

// apply makes a step that needs nothing beyond the draft and its source.
func (src *editSource) apply(state draftState, op DraftOp) (draftState, string, error) {
	next := state.clone()
	switch op.Type {
	case "replace":
		i, err := shotAt(state, op.Shot)
		if err != nil {
			return draftState{}, "", err
		}
		if _, _, ok := src.beat(op.Beat); !ok {
			return draftState{}, "", ErrInvalid
		}
		if err := src.fits(state, i, op.Beat); err != nil {
			return draftState{}, "", err
		}
		next.Shots[i].Beat = op.Beat
		return next, fmt.Sprintf("第 %d 镜已替换", op.Shot), nil
	case "move":
		return src.move(state, op.Shot, op.To)
	case "lock", "unlock":
		i, err := shotAt(state, op.Shot)
		if err != nil {
			return draftState{}, "", err
		}
		next.Shots[i].Locked = op.Type == "lock"
		if op.Type == "lock" {
			return next, fmt.Sprintf("第 %d 镜已锁定", op.Shot), nil
		}
		return next, fmt.Sprintf("第 %d 镜已解锁", op.Shot), nil
	case "revert":
		i, err := shotAt(state, op.Shot)
		if err != nil {
			return draftState{}, "", err
		}
		shot := state.Shots[i]
		if !state.changed(shot) {
			return draftState{}, "", refuse("第 %d 镜没有改过", op.Shot)
		}
		original := state.original(shot)
		if err := src.fits(state, i, original); err != nil {
			return draftState{}, "", err
		}
		next.Shots[i].Beat = original
		return next, fmt.Sprintf("第 %d 镜已还原", op.Shot), nil
	case "reset":
		fresh := src.newDraftState()
		fresh.Slots = state.Slots
		return fresh, "已全部还原", nil
	}
	return draftState{}, "", ErrInvalid
}

// fits checks that beat can lead shot i: not another shot's, and long
// enough for the shot's slots slowed as the fill may slow it.
func (src *editSource) fits(state draftState, i int, beat string) error {
	if used := state.usedBy(beat); used != 0 && used != i+1 {
		return refuse("这个节拍已用在第 %d 镜", used)
	}
	if !src.covers(beat, state.length(state.Shots[i]), slowMost) {
		return refuse("这个节拍不够第 %d 镜的 %.1f 秒", i+1, state.length(state.Shots[i]))
	}
	return nil
}

// move moves shot from among its part's shots to where shot to is. The
// locked shots stay; the others take the unlocked places in their new
// order (docs/specs/montage-center.md, 拖动).
func (src *editSource) move(state draftState, from, to int) (draftState, string, error) {
	i, err := shotAt(state, from)
	if err != nil {
		return draftState{}, "", err
	}
	j, err := shotAt(state, to)
	if err != nil {
		return draftState{}, "", err
	}
	part := state.Slots[state.Shots[i].First].Part
	if state.Slots[state.Shots[j].First].Part != part {
		return draftState{}, "", refuse("只能在同一个段落里换顺序")
	}
	if state.Shots[i].Locked || state.Shots[j].Locked {
		return draftState{}, "", refuse("锁定的镜头不动")
	}
	if i == j {
		return draftState{}, "", refuse("位置没有变")
	}
	var places []int // the part's unlocked shots, in film order
	for k, shot := range state.Shots {
		if state.Slots[shot.First].Part == part && !shot.Locked {
			places = append(places, k)
		}
	}
	moving := make([]draftShot, len(places))
	for n, k := range places {
		moving[n] = state.Shots[k]
	}
	a, b := slices.Index(places, i), slices.Index(places, j)
	shot := moving[a]
	moving = slices.Delete(moving, a, a+1)
	moving = slices.Insert(moving, b, shot)
	next := state.clone()
	for n, k := range places {
		next.Shots[k].Beat = moving[n].Beat
		if !src.covers(moving[n].Beat, state.length(next.Shots[k]), slowMost) {
			return draftState{}, "", refuse("换到第 %d 镜位置的镜头不够 %.1f 秒", k+1, state.length(next.Shots[k]))
		}
	}
	return next, fmt.Sprintf("第 %d 镜已换到第 %d 镜的位置", from, to), nil
}

// deleteShot takes a shot out: the one before or after it takes its slots,
// whichever card covers both; when both do, the one that looks more like
// it (likeness). A film keeps at least one shot.
func (s *Store) deleteShot(ctx context.Context, src *editSource, state draftState, n int) (draftState, string, error) {
	i, err := shotAt(state, n)
	if err != nil {
		return draftState{}, "", err
	}
	if len(state.Shots) == 1 {
		return draftState{}, "", refuse("至少要留一个镜头")
	}
	gone := state.Shots[i]
	var sides []int
	for _, k := range []int{i - 1, i + 1} {
		if k < 0 || k >= len(state.Shots) {
			continue
		}
		neighbour := state.Shots[k]
		length := state.Slots[max(gone.Last, neighbour.Last)].End - state.Slots[min(gone.First, neighbour.First)].Start
		if src.covers(neighbour.Beat, length, 1) {
			sides = append(sides, k)
		}
	}
	if len(sides) == 0 {
		return draftState{}, "", refuse("前后两镜都不够长，换一个镜头代替")
	}
	keep := sides[0]
	if len(sides) == 2 {
		before, err := s.likeness(ctx, src, gone.Beat, state.Shots[sides[0]].Beat)
		if err != nil {
			return draftState{}, "", err
		}
		after, err := s.likeness(ctx, src, gone.Beat, state.Shots[sides[1]].Beat)
		if err != nil {
			return draftState{}, "", err
		}
		if after > before {
			keep = sides[1]
		}
	}
	next := state.clone()
	next.Shots[keep].First = min(next.Shots[keep].First, gone.First)
	next.Shots[keep].Last = max(next.Shots[keep].Last, gone.Last)
	next.Shots = slices.Delete(next.Shots, i, i+1)
	if keep < i {
		return next, "已删除，由前一镜延长补上", nil
	}
	return next, "已删除，由后一镜延长补上", nil
}

// likeness is how alike two beats look: the compute Worker's cosine of the
// mean window vectors each covers.
func (s *Store) likeness(ctx context.Context, src *editSource, a, b string) (float64, error) {
	if s.jobCompute == nil {
		return 0, ErrJobsUnavailable
	}
	va, dimensions, err := s.beatVectors(ctx, src, a)
	if err != nil {
		return 0, err
	}
	vb, other, err := s.beatVectors(ctx, src, b)
	if err != nil {
		return 0, err
	}
	if dimensions != other {
		return 0, fmt.Errorf("window vectors of %d and %d dimensions", dimensions, other)
	}
	alike, err := s.jobCompute.Likeness(ctx, va, vb, dimensions)
	if errors.Is(err, ErrWorkerRefused) {
		return 0, fmt.Errorf("%w: %w", ErrWorkerFailed, err)
	}
	return alike, err
}

// beatVectors are the vectors of the windows a beat covers.
func (s *Store) beatVectors(ctx context.Context, src *editSource, beat string) ([]float32, int, error) {
	video, start, end := src.span(beat)
	analysis, raw, err := s.grants.Analysis(ctx, src.grantID, video)
	if err != nil {
		return nil, 0, grantError(err)
	}
	dimensions := analysis.Dimensions
	if dimensions <= 0 || dimensions > photos.MaxWindowDimensions || len(raw) != len(analysis.List)*4*dimensions {
		return nil, 0, fmt.Errorf("%d bytes of vectors for %d windows of dimension %d", len(raw), len(analysis.List), dimensions)
	}
	var vectors []float32
	for i, window := range analysis.List {
		if float64(window.StartMs)/1000 >= end || float64(window.EndMs)/1000 <= start {
			continue
		}
		for d := range dimensions {
			at := (i*dimensions + d) * 4
			vectors = append(vectors, math.Float32frombits(binary.LittleEndian.Uint32(raw[at:])))
		}
	}
	if len(vectors) == 0 {
		return nil, 0, fmt.Errorf("no windows beneath beat %s", beat)
	}
	return vectors, dimensions, nil
}

// --- searching ---

// WindowSearch finds, among a grant's videos, the windows most like each of
// texts: the photo library's searchWindows (#599). It answers ErrSearchModel
// when the windows were analysed with another model than the text's.
type WindowSearch interface {
	SearchWindows(ctx context.Context, grantID string, texts []string, k int) ([][]WindowHit, error)
}

// WindowHit is a window like a text: its video, its number among the
// video's windows, and how alike.
type WindowHit struct {
	AssetID string  `json:"assetId"`
	Window  int     `json:"window"`
	Score   float64 `json:"score"`
}

// searchWindows is how many windows a text asks for.
const searchWindows = 200

// scoredBeat is a beat a search found, by the best of its windows.
type scoredBeat struct {
	beat  string
	score float64
}

// searchBeats are the beats most like each of texts, best first: a beat
// scores its best window (docs/architecture/montage-center.md, 候选).
func (s *Store) searchBeats(ctx context.Context, src *editSource, texts []string) ([][]scoredBeat, error) {
	if s.search == nil {
		return nil, ErrSearchUnavailable
	}
	hits, err := s.search.SearchWindows(ctx, src.grantID, texts, searchWindows)
	if err != nil {
		return nil, err
	}
	if len(hits) != len(texts) {
		return nil, fmt.Errorf("the search answered %d lists for %d texts", len(hits), len(texts))
	}
	// Each video's windows by number, and its beats in the catalog.
	windows := map[string]map[int]photos.Window{}
	for id, video := range src.videos {
		windows[id] = map[int]photos.Window{}
		for _, window := range video.Windows {
			windows[id][window.Index] = window
		}
	}
	beats := map[string][]string{}
	for _, id := range src.catalog {
		card := src.cards[id]
		for n := range card.Times {
			beats[card.Video] = append(beats[card.Video], fmt.Sprintf("%s/%d", id, n+1))
		}
	}
	answer := make([][]scoredBeat, len(texts))
	for t, list := range hits {
		best := map[string]float64{}
		for _, hit := range list {
			window, ok := windows[hit.AssetID][hit.Window]
			if !ok || math.IsNaN(hit.Score) {
				continue
			}
			start, end := float64(window.StartMs)/1000, float64(window.EndMs)/1000
			for _, beat := range beats[hit.AssetID] {
				_, a, b := src.span(beat)
				if a < end && b > start {
					if score, seen := best[beat]; !seen || hit.Score > score {
						best[beat] = hit.Score
					}
				}
			}
		}
		for beat, score := range best {
			answer[t] = append(answer[t], scoredBeat{beat: beat, score: score})
		}
		sort.Slice(answer[t], func(a, b int) bool {
			x, y := answer[t][a], answer[t][b]
			return x.score > y.score || x.score == y.score && x.beat < y.beat
		})
	}
	return answer, nil
}

// shuffleFrom is how many of the most alike beats a shuffle picks from.
const shuffleFrom = 4

// shuffle picks each unlocked shot again from the four beats most like its
// picture's description, by seed: not one used in the film, not of the
// card before it, long enough, and not 竖 where the shot was not.
func (s *Store) shuffle(ctx context.Context, src *editSource, state draftState, seed int64) (draftState, string, error) {
	var texts []string
	locked := 0
	for _, shot := range state.Shots {
		if shot.Locked {
			locked++
			continue
		}
		slot := state.Slots[shot.First]
		if query := src.query(slot.Part, slot.Picture); !slices.Contains(texts, query) {
			texts = append(texts, query)
		}
	}
	if locked == len(state.Shots) {
		return draftState{}, "", refuse("镜头都锁定了，没有可以重洗的")
	}
	found, err := s.searchBeats(ctx, src, texts)
	if err != nil {
		return draftState{}, "", err
	}
	//nolint:gosec // G404: the seed makes a shuffle again as it was (kept with its revision); nothing rests on guessing it.
	random := mathrand.New(mathrand.NewPCG(uint64(seed), bits.ReverseBytes64(uint64(seed))))
	next := state.clone()
	used := map[string]bool{}
	for _, shot := range state.Shots {
		if shot.Locked {
			used[shot.Beat] = true
		}
	}
	changed := 0
	for i, shot := range state.Shots {
		if shot.Locked {
			continue
		}
		slot := state.Slots[shot.First]
		ranked := found[slices.Index(texts, src.query(slot.Part, slot.Picture))]
		var before string
		if i > 0 {
			before = cardOf(next.Shots[i-1].Beat)
		}
		var after string
		if i+1 < len(next.Shots) && next.Shots[i+1].Locked {
			after = cardOf(next.Shots[i+1].Beat)
		}
		landscape := !src.portrait(shot.Beat)
		var picks []string
		for _, candidate := range ranked {
			if len(picks) == shuffleFrom {
				break
			}
			card := cardOf(candidate.beat)
			if used[candidate.beat] || card == before || card == after || (landscape && src.portrait(candidate.beat)) ||
				!src.covers(candidate.beat, state.length(shot), slowMost) || reservedLater(state, i, candidate.beat) {
				continue
			}
			picks = append(picks, candidate.beat)
		}
		if len(picks) > 0 {
			next.Shots[i].Beat = picks[random.IntN(len(picks))]
		}
		used[next.Shots[i].Beat] = true
		if next.Shots[i].Beat != shot.Beat {
			changed++
		}
	}
	return next, fmt.Sprintf("换了 %d 个镜头，锁住的 %d 个没动", changed, locked), nil
}

// reservedLater reports whether beat leads a shot after i: that shot may
// find nothing to pick and keep it.
func reservedLater(state draftState, i int, beat string) bool {
	return slices.ContainsFunc(state.Shots[i+1:], func(shot draftShot) bool { return shot.Beat == beat })
}

func cardOf(beat string) string {
	if card, _, found := strings.CutLast(beat, "/"); found {
		return card
	}
	return beat
}
