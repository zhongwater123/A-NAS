package app

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"math/bits"
	mathrand "math/rand/v2"
	"slices"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
)

// The rules of each step (ADR 0039, decision 2;
// docs/architecture/montage-center.md, 出片后的二次编辑: 修改稿): the
// unit is the music's slot, whose length and cuts stay; a beat is used once
// in a film; the cards must be long enough.

// randomSeed is the seed of a shuffle the member gave none for.
func randomSeed() int64 {
	//nolint:gosec // G404: the seed only picks among a shuffle's candidates and is kept with its revision; nothing rests on guessing it.
	return mathrand.Int64()
}

// shotAt is shot number n of state, from 1.
func shotAt(state draftState, n int) (int, error) {
	if n < 1 || n > len(state.Shots) {
		return 0, domain.ErrInvalid
	}
	return n - 1, nil
}

// apply makes a step that needs nothing beyond the draft and its source.
func (src *editSource) apply(state draftState, op domain.DraftOp) (draftState, string, error) {
	next := state.clone()
	switch op.Type {
	case "replace":
		i, err := shotAt(state, op.Shot)
		if err != nil {
			return draftState{}, "", err
		}
		card, _, ok := src.beat(op.Beat)
		if !ok {
			return draftState{}, "", domain.ErrInvalid
		}
		if !src.cuttable(card.ID) {
			return draftState{}, "", refuse("这个节拍不在这一版能用的素材里")
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
		return src.newDraftState(), "已全部还原", nil
	}
	return draftState{}, "", domain.ErrInvalid
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
// order (ADR 0039, decision 2: 拖动).
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
		vectors := s.beatVectors(ctx, src)
		before, err := s.likeness(ctx, vectors, gone.Beat, state.Shots[sides[0]].Beat)
		if err != nil {
			return draftState{}, "", err
		}
		after, err := s.likeness(ctx, vectors, gone.Beat, state.Shots[sides[1]].Beat)
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
// mean window vectors each covers, which vectors reads.
func (s *Store) likeness(ctx context.Context, vectors func(beat string) ([]float32, int, error), a, b string) (float64, error) {
	if s.jobCompute == nil {
		return 0, domain.ErrJobsUnavailable
	}
	va, dimensions, err := vectors(a)
	if err != nil {
		return 0, err
	}
	vb, other, err := vectors(b)
	if err != nil {
		return 0, err
	}
	if dimensions != other {
		return 0, fmt.Errorf("window vectors of %d and %d dimensions", dimensions, other)
	}
	return s.jobCompute.Likeness(ctx, va, vb, dimensions)
}

// beatVectors reads the vectors of the windows a beat covers, and their
// dimensions. A video's analysis comes with all its windows' vectors, so
// it is read once for all the beats asked of it: the shots beside a
// deleted one are mostly of its video.
func (s *Store) beatVectors(ctx context.Context, src *editSource) func(beat string) ([]float32, int, error) {
	type analysed struct {
		analysis photos.GrantAnalysis
		raw      []byte
	}
	read := map[string]analysed{}
	return func(beat string) ([]float32, int, error) {
		video, start, end := src.span(beat)
		of, ok := read[video]
		if !ok {
			analysis, raw, err := s.grants.Analysis(ctx, src.grantID, video)
			if assetGone(err) {
				// Since the version was made: a refusal of this step.
				return nil, 0, refuse("这一镜或前后镜头的视频已从相册删除或放进回收站，不能删这一镜")
			}
			if err != nil {
				return nil, 0, grantError(err)
			}
			// Windows being made again have no vectors yet, and those made
			// with another model are not the version's (checkAnalysis).
			was := src.videos[video]
			if analysis.ObjectID == was.ObjectID && analysis.Windows != photos.AnalysisDone {
				return nil, 0, refuse("这一镜或前后镜头的视频正在重新分析，请稍后再删")
			}
			if was.windowsChanged(analysis) {
				return nil, 0, domain.ErrAnalysisChanged
			}
			of = analysed{analysis: analysis, raw: raw}
			read[video] = of
		}
		_, vectors, err := windowVectors(of.analysis, of.raw, func(from, to float64) bool { return from < end && to > start })
		if err != nil {
			return nil, 0, err
		}
		if len(vectors) == 0 {
			return nil, 0, fmt.Errorf("no windows beneath beat %s", beat)
		}
		return vectors, of.analysis.Dimensions, nil
	}
}

// --- searching ---

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
		return nil, domain.ErrSearchUnavailable
	}
	hits, err := s.search.SearchWindows(ctx, src.grantID, texts, searchWindows)
	if err != nil {
		return nil, err
	}
	if len(hits) != len(texts) {
		return nil, fmt.Errorf("the search answered %d lists for %d texts", len(hits), len(texts))
	}
	// Each video's windows by number, and its beats in the catalog with
	// the part of the video each covers.
	type beatSpan struct {
		beat       string
		start, end float64
	}
	windows := map[string]map[int]photos.Window{}
	for id, video := range src.videos {
		windows[id] = map[int]photos.Window{}
		for _, window := range video.Windows {
			windows[id][window.Index] = window
		}
	}
	beats := map[string][]beatSpan{}
	for _, id := range src.catalog {
		card := src.cards[id]
		for n := range card.Times {
			beat := beatID(id, n+1)
			_, from, to := src.span(beat)
			beats[card.Video] = append(beats[card.Video], beatSpan{beat: beat, start: from, end: to})
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
			for _, of := range beats[hit.AssetID] {
				if of.start < end && of.end > start {
					if score, seen := best[of.beat]; !seen || hit.Score > score {
						best[of.beat] = hit.Score
					}
				}
			}
		}
		for beat, score := range best {
			answer[t] = append(answer[t], scoredBeat{beat: beat, score: score})
		}
		slices.SortFunc(answer[t], func(a, b scoredBeat) int {
			return cmp.Or(cmp.Compare(b.score, a.score), cmp.Compare(a.beat, b.beat))
		})
	}
	return answer, nil
}

// shuffleFrom is how many of the most alike beats a shuffle picks from.
const shuffleFrom = 4

// shuffle picks each unlocked shot again from the four beats most like its
// picture's description, by seed: not one used in the film, not of the
// card before it nor of the next shot's as it is, long enough, and not 竖 where the shot was not. A shot
// whose description the plan left empty or too long to search for stays as
// it is: the plan wrote it, not the member, so it is not their request that
// is wrong.
func (s *Store) shuffle(ctx context.Context, src *editSource, state draftState, seed int64) (draftState, string, error) {
	var texts []string
	// asks is which of texts each shot is picked again from; -1 for a shot
	// that stays as it is, locked or with nothing to search for.
	asks := make([]int, len(state.Shots))
	locked, unsearched := 0, 0
	for i, shot := range state.Shots {
		asks[i] = -1
		if shot.Locked {
			locked++
			continue
		}
		slot := state.Slots[shot.First]
		query := src.query(slot.Part, slot.Picture)
		if !searchable(query) {
			unsearched++
			continue
		}
		if asks[i] = slices.Index(texts, query); asks[i] < 0 {
			asks[i] = len(texts)
			texts = append(texts, query)
		}
	}
	if locked == len(state.Shots) {
		return draftState{}, "", refuse("镜头都锁定了，没有可以重洗的")
	}
	if len(texts) == 0 {
		return draftState{}, "", refuse("没锁的镜头在规划里都没有可以搜索的画面描述，重洗换不了")
	}
	found, err := s.searchBeats(ctx, src, texts)
	if err != nil {
		return draftState{}, "", err
	}
	//nolint:gosec // G404: the seed makes a shuffle again as it was (kept with its revision); nothing rests on guessing it.
	random := mathrand.New(mathrand.NewPCG(uint64(seed), bits.ReverseBytes64(uint64(seed))))
	next := state.clone()
	changed := 0
	for i, shot := range state.Shots {
		if asks[i] < 0 {
			continue
		}
		ranked := found[asks[i]]
		var before string
		if i > 0 {
			before = cardOf(next.Shots[i-1].Beat)
		}
		var after string
		if i+1 < len(next.Shots) {
			// Nor of the next shot's card: that shot may stay as it is,
			// locked, with nothing to search for, or finding nothing to pick
			// (as taken keeps its beat).
			after = cardOf(next.Shots[i+1].Beat)
		}
		landscape := !src.portrait(shot.Beat)
		var picks []string
		for _, candidate := range ranked {
			if len(picks) == shuffleFrom {
				break
			}
			card := cardOf(candidate.beat)
			if taken(next, i, candidate.beat) || card == before || card == after || (landscape && src.portrait(candidate.beat)) ||
				!src.covers(candidate.beat, state.length(shot), slowMost) {
				continue
			}
			picks = append(picks, candidate.beat)
		}
		if len(picks) > 0 {
			next.Shots[i].Beat = picks[random.IntN(len(picks))]
		}
		if next.Shots[i].Beat != shot.Beat {
			changed++
		}
	}
	message := fmt.Sprintf("换了 %d 个镜头，锁住的 %d 个没动", changed, locked)
	if unsearched > 0 {
		message += fmt.Sprintf("，%d 个在规划里没有可以搜索的画面描述，保持原样", unsearched)
	}
	return next, message, nil
}

// searchable reports whether the photo library searches for query
// (photos.QueryText).
func searchable(query string) bool {
	_, ok := photos.QueryText(query)
	return ok
}

// taken reports whether beat leads a shot of a shuffle other than the i-th:
// one before it as shuffled, or one after it as it is, which may stay so
// (locked, with nothing to search for, or finding nothing to pick).
func taken(next draftState, i int, beat string) bool {
	leads := func(shot draftShot) bool { return shot.Beat == beat }
	return slices.ContainsFunc(next.Shots[:i], leads) || slices.ContainsFunc(next.Shots[i+1:], leads)
}

// cardOf is the card of a beat.
func cardOf(beat string) string {
	if card, _, ok := splitBeat(beat); ok {
		return card
	}
	return beat
}
