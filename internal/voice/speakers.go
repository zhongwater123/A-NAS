package voice

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// Voiceprint rules, from the evaluation on AISHELL-4's far-field meetings
// (docs/research/voiceprint-evaluation.md). Scores are the cosine of two
// speakers' profiles.
const (
	// AutoThreshold names a speaker after a person without asking: no
	// member of a group was named after another there, whether they spoke
	// for 2 minutes, 30 s or 10 s.
	AutoThreshold = 0.74
	// SuggestThreshold offers the person as "可能是": the one same-person
	// pair below it was a 10 s profile that scored 0.24.
	SuggestThreshold = 0.60
	// SameVoiceThreshold joins the speakers of two pieces of a long
	// recording into one. Joining the halves of each meeting this way made
	// no wrong join with 30 s or more of speech and left 2–6 % of the
	// voices apart, which 合并发言人 puts together.
	SameVoiceThreshold = 0.70
	// ClusterDistance groups sentences by voice when the recognition
	// service told no speakers apart: cosine distance below it is one voice.
	ClusterDistance = 1 - 0.50
)

// What a profile is made of: a speaker's sentences of MinStretch to
// MaxStretch each, longest first, ProfileBudget in all.
const (
	MinStretchMs  = 1000
	MaxStretchMs  = 15000
	ProfileBudget = 120000
	// ClusterStretchMs caps each sentence a clustering embeds, and
	// ClusterBudget all of them.
	ClusterStretchMs = 8000
	ClusterBudget    = 25 * 60 * 1000
)

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot
}

// blend is the unit vector of a and b weighted by how much speech each holds.
func blend(a []float32, aMs int64, b []float32, bMs int64) []float32 {
	if len(a) == 0 {
		return b
	}
	if len(b) != len(a) {
		return a
	}
	mixed := make([]float32, len(a))
	var norm float64
	for i := range a {
		value := float64(a[i])*float64(aMs) + float64(b[i])*float64(bMs)
		mixed[i] = float32(value)
		norm += value * value
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		return a
	}
	for i := range mixed {
		mixed[i] = float32(float64(mixed[i]) / norm)
	}
	return mixed
}

// stretch is part of a sentence chosen for a profile, in milliseconds from
// the start of its piece of the recording.
type stretch struct {
	startMs, endMs int64
	label          string
	utterance      int
}

// profileStretches chooses, for one voice's sentences, the stretches its
// profile is made of: budget milliseconds at most.
func profileStretches(utterances []Recognized, indexes []int, label string, budget int64) []stretch {
	sorted := append([]int(nil), indexes...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return utterances[sorted[i]].EndMs-utterances[sorted[i]].StartMs > utterances[sorted[j]].EndMs-utterances[sorted[j]].StartMs
	})
	var chosen []stretch
	var total int64
	for _, index := range sorted {
		utterance := utterances[index]
		length := min(utterance.EndMs-utterance.StartMs, MaxStretchMs, budget-total)
		if length < MinStretchMs {
			break
		}
		chosen = append(chosen, stretch{startMs: utterance.StartMs, endMs: utterance.StartMs + length, label: label, utterance: index})
		total += length
	}
	return chosen
}

// clusterStretches chooses up to ClusterBudget of every sentence long enough
// to embed, spread over the piece when there is more.
func clusterStretches(utterances []Recognized) []stretch {
	var candidates []stretch
	var total int64
	for index, utterance := range utterances {
		length := min(utterance.EndMs-utterance.StartMs, ClusterStretchMs)
		if length < MinStretchMs {
			continue
		}
		candidates = append(candidates, stretch{startMs: utterance.StartMs, endMs: utterance.StartMs + length, utterance: index})
		total += length
	}
	if total <= ClusterBudget {
		return candidates
	}
	// Every k-th, so the whole piece is heard.
	keep := float64(ClusterBudget) / float64(total)
	var chosen []stretch
	var carried float64
	for _, candidate := range candidates {
		carried += keep
		if carried >= 1 {
			carried--
			chosen = append(chosen, candidate)
		}
	}
	return chosen
}

// profiled is one voice of a recording as the pipeline found it.
type profiled struct {
	key       string
	number    int
	profile   []float32
	profileMs int64
	speechMs  int64
}

// person is a named person's voiceprint: the profiles of the speakers the
// member named after them.
type namedPerson struct {
	id      string
	name    string
	samples [][]float32
}

// match is the best person for a profile and how close they are.
func match(profile []float32, people []namedPerson, rejected map[string]bool) (string, float64) {
	bestID, best := "", -1.0
	for _, person := range people {
		if rejected[person.id] {
			continue
		}
		for _, sample := range person.samples {
			if score := cosine(profile, sample); score > best {
				bestID, best = person.id, score
			}
		}
	}
	return bestID, best
}

// rematch names, suggests or clears every speaker of the member's that the
// member did not name, after their people or voiceprints changed. Recordings
// whose names change get a new names revision, so their summaries show as
// out of date.
func (s *Service) rematch(ctx context.Context, ownerID string) error {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	people := map[string]*namedPerson{}
	var order []string
	rows, err := tx.QueryContext(ctx, "SELECT id, name FROM people WHERE owner_id = ? ORDER BY created_at", ownerID)
	if err != nil {
		return err
	}
	for rows.Next() {
		person := &namedPerson{}
		if err := rows.Scan(&person.id, &person.name); err != nil {
			_ = rows.Close()
			return err
		}
		people[person.id] = person
		order = append(order, person.id)
	}
	_ = rows.Close()
	rows, err = tx.QueryContext(ctx, `
SELECT s.person_id, s.profile FROM speakers s JOIN recordings r ON r.id = s.recording_id
WHERE r.owner_id = ? AND s.assigned = ? AND s.person_id IS NOT NULL AND s.profile IS NOT NULL AND r.voiceprint_model = ?`,
		ownerID, AssignedUser, s.currentVoiceModel(ctx, tx, ownerID))
	if err != nil {
		return err
	}
	for rows.Next() {
		var personID string
		var profile []byte
		if err := rows.Scan(&personID, &profile); err != nil {
			_ = rows.Close()
			return err
		}
		if person, ok := people[personID]; ok {
			person.samples = append(person.samples, decodeVector(profile))
		}
	}
	_ = rows.Close()
	list := make([]namedPerson, 0, len(order))
	for _, id := range order {
		if len(people[id].samples) > 0 {
			list = append(list, *people[id])
		}
	}
	type candidate struct {
		recording, key, person, assigned, suggested string
		profile                                     []float32
		score, suggestedScore                       float64
	}
	var candidates []candidate
	rows, err = tx.QueryContext(ctx, `
SELECT s.recording_id, s.key, COALESCE(s.person_id, ''), s.assigned, COALESCE(s.suggested_id, ''), s.score, s.suggested_score, s.profile
FROM speakers s JOIN recordings r ON r.id = s.recording_id
WHERE r.owner_id = ? AND s.assigned != ? AND s.profile IS NOT NULL`, ownerID, AssignedUser)
	if err != nil {
		return err
	}
	for rows.Next() {
		var value candidate
		var profile []byte
		if err := rows.Scan(&value.recording, &value.key, &value.person, &value.assigned, &value.suggested, &value.score, &value.suggestedScore, &profile); err != nil {
			_ = rows.Close()
			return err
		}
		value.profile = decodeVector(profile)
		candidates = append(candidates, value)
	}
	_ = rows.Close()
	rejected := map[[2]string]map[string]bool{}
	rows, err = tx.QueryContext(ctx, `
SELECT j.recording_id, j.key, j.person_id FROM rejections j JOIN recordings r ON r.id = j.recording_id WHERE r.owner_id = ?`, ownerID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var recording, key, person string
		if err := rows.Scan(&recording, &key, &person); err != nil {
			_ = rows.Close()
			return err
		}
		if rejected[[2]string{recording, key}] == nil {
			rejected[[2]string{recording, key}] = map[string]bool{}
		}
		rejected[[2]string{recording, key}][person] = true
	}
	_ = rows.Close()
	changed := map[string]bool{}
	for _, value := range candidates {
		personID, score := match(value.profile, list, rejected[[2]string{value.recording, value.key}])
		person, assigned, suggested, suggestedScore := "", "", "", 0.0
		switch {
		case personID != "" && score >= AutoThreshold:
			person, assigned = personID, AssignedAuto
		case personID != "" && score >= SuggestThreshold:
			suggested, suggestedScore, score = personID, score, 0
		default:
			score = 0
		}
		if person == value.person && assigned == value.assigned && suggested == value.suggested &&
			math.Abs(score-value.score) < 1e-9 && math.Abs(suggestedScore-value.suggestedScore) < 1e-9 {
			continue
		}
		if person != value.person {
			changed[value.recording] = true
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE speakers SET person_id = NULLIF(?, ''), assigned = ?, score = ?, suggested_id = NULLIF(?, ''), suggested_score = ?
WHERE recording_id = ? AND key = ?`, person, assigned, score, suggested, suggestedScore, value.recording, value.key); err != nil {
			return err
		}
	}
	for recording := range changed {
		if _, err := tx.ExecContext(ctx, "UPDATE recordings SET names_revision = names_revision + 1 WHERE id = ?", recording); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// currentVoiceModel is the model whose voiceprints can be compared: the
// Worker's when known, else the one the member's newest recording used.
// Profiles from another model are not comparable.
func (s *Service) currentVoiceModel(ctx context.Context, tx *sql.Tx, ownerID string) string {
	s.mu.Lock()
	model := s.voiceModel
	s.mu.Unlock()
	if model != "" {
		return model
	}
	_ = tx.QueryRowContext(ctx, `SELECT voiceprint_model FROM recordings WHERE owner_id = ? AND voiceprint_model != ''
ORDER BY processed_at DESC LIMIT 1`, ownerID).Scan(&model)
	return model
}

// AssignRequest names a speaker: after an existing person (PersonID) or by
// a name, which finds or creates the person.
type AssignRequest struct {
	PersonID string `json:"personId"`
	Name     string `json:"name"`
}

func cleanName(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" || utf8.RuneCountInString(name) > maxNameRunes {
		return "", fmt.Errorf("%w: a name has 1 to %d characters", ErrInvalid, maxNameRunes)
	}
	return name, nil
}

// AssignSpeaker names a speaker, which teaches the person's voiceprint and
// may name the same voice in the member's other recordings.
func (s *Service) AssignSpeaker(ctx context.Context, actor accounts.User, id, key string, request AssignRequest) (Detail, error) {
	if _, err := s.readySpeaker(ctx, actor, id, key); err != nil {
		return Detail{}, err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Detail{}, err
	}
	defer tx.Rollback()
	personID, err := s.findOrCreatePerson(ctx, tx, actor, request)
	if err != nil {
		return Detail{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE speakers SET person_id = ?, assigned = ?, score = 0, suggested_id = NULL, suggested_score = 0 WHERE recording_id = ? AND key = ?`,
		personID, AssignedUser, id, key); err != nil {
		return Detail{}, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM rejections WHERE recording_id = ? AND key = ? AND person_id = ?", id, key, personID); err != nil {
		return Detail{}, err
	}
	if err := s.touchNames(ctx, tx, id); err != nil {
		return Detail{}, err
	}
	if err := tx.Commit(); err != nil {
		return Detail{}, err
	}
	if err := s.rematch(ctx, actor.ID); err != nil {
		return Detail{}, err
	}
	return s.Get(ctx, actor, id)
}

func (s *Service) findOrCreatePerson(ctx context.Context, tx *sql.Tx, actor accounts.User, request AssignRequest) (string, error) {
	if request.PersonID != "" {
		var found string
		err := tx.QueryRowContext(ctx, "SELECT id FROM people WHERE id = ? AND owner_id = ?", request.PersonID, actor.ID).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return found, err
	}
	name, err := cleanName(request.Name)
	if err != nil {
		return "", err
	}
	var found string
	err = tx.QueryRowContext(ctx, "SELECT id FROM people WHERE owner_id = ? AND name = ?", actor.ID, name).Scan(&found)
	if err == nil {
		return found, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	found = s.randomID("person")
	now := formatTime(s.now())
	_, err = tx.ExecContext(ctx, "INSERT INTO people (id, owner_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)", found, actor.ID, name, now, now)
	return found, err
}

func (s *Service) touchNames(ctx context.Context, tx *sql.Tx, recordingID string) error {
	_, err := tx.ExecContext(ctx, "UPDATE recordings SET names_revision = names_revision + 1, updated_at = ? WHERE id = ?", formatTime(s.now()), recordingID)
	return err
}

// readySpeaker checks that the recording is the member's, has a transcript
// and has the speaker.
func (s *Service) readySpeaker(ctx context.Context, actor accounts.User, id, key string) (Recording, error) {
	recording, err := s.owned(ctx, actor, id)
	if err != nil {
		return Recording{}, err
	}
	if recording.Status != StatusReady {
		return Recording{}, ErrNotReady
	}
	var count int
	if err := s.store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM speakers WHERE recording_id = ? AND key = ?", id, key).Scan(&count); err != nil {
		return Recording{}, err
	}
	if count == 0 {
		return Recording{}, ErrNotFound
	}
	return recording, nil
}

// RejectPerson says a speaker is not that person: a name or suggestion of
// theirs goes, and recognition never offers them for this speaker again.
func (s *Service) RejectPerson(ctx context.Context, actor accounts.User, id, key, personID string) (Detail, error) {
	if _, err := s.readySpeaker(ctx, actor, id, key); err != nil {
		return Detail{}, err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Detail{}, err
	}
	defer tx.Rollback()
	var owned int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM people WHERE id = ? AND owner_id = ?", personID, actor.ID).Scan(&owned); err != nil {
		return Detail{}, err
	}
	if owned == 0 {
		return Detail{}, ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO rejections (recording_id, key, person_id) VALUES (?, ?, ?)", id, key, personID); err != nil {
		return Detail{}, err
	}
	result, err := tx.ExecContext(ctx, "UPDATE speakers SET person_id = NULL, assigned = '', score = 0 WHERE recording_id = ? AND key = ? AND person_id = ?", id, key, personID)
	if err != nil {
		return Detail{}, err
	}
	if count, _ := result.RowsAffected(); count > 0 {
		if err := s.touchNames(ctx, tx, id); err != nil {
			return Detail{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE speakers SET suggested_id = NULL, suggested_score = 0 WHERE recording_id = ? AND key = ? AND suggested_id = ?", id, key, personID); err != nil {
		return Detail{}, err
	}
	if err := tx.Commit(); err != nil {
		return Detail{}, err
	}
	if err := s.rematch(ctx, actor.ID); err != nil {
		return Detail{}, err
	}
	return s.Get(ctx, actor, id)
}

// MergeSpeakers makes two voices of a recording one: the recognition service
// sometimes hears one person as two.
func (s *Service) MergeSpeakers(ctx context.Context, actor accounts.User, id, key, into string) (Detail, error) {
	if key == into {
		return Detail{}, fmt.Errorf("%w: a speaker merges into another", ErrInvalid)
	}
	if _, err := s.readySpeaker(ctx, actor, id, key); err != nil {
		return Detail{}, err
	}
	if _, err := s.readySpeaker(ctx, actor, id, into); err != nil {
		return Detail{}, err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Detail{}, err
	}
	defer tx.Rollback()
	load := func(which string) (string, string, []float32, int64, error) {
		var person, assigned string
		var profile []byte
		var profileMs int64
		err := tx.QueryRowContext(ctx, "SELECT COALESCE(person_id, ''), assigned, profile, profile_ms FROM speakers WHERE recording_id = ? AND key = ?", id, which).
			Scan(&person, &assigned, &profile, &profileMs)
		return person, assigned, decodeVector(profile), profileMs, err
	}
	fromPerson, fromAssigned, fromProfile, fromMs, err := load(key)
	if err != nil {
		return Detail{}, err
	}
	intoPerson, intoAssigned, intoProfile, intoMs, err := load(into)
	if err != nil {
		return Detail{}, err
	}
	// The kept voice keeps its name; it takes the other's only if it had none
	// or only a recognised one and the other was named by the member.
	person, assigned := intoPerson, intoAssigned
	if fromAssigned == AssignedUser && intoAssigned != AssignedUser || person == "" {
		person, assigned = fromPerson, fromAssigned
	}
	profile := blend(intoProfile, intoMs, fromProfile, fromMs)
	if _, err := tx.ExecContext(ctx, "UPDATE utterances SET speaker = ? WHERE recording_id = ? AND speaker = ?", into, id, key); err != nil {
		return Detail{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE speakers SET person_id = NULLIF(?, ''), assigned = ?, profile = ?, profile_ms = ?, suggested_id = NULL, suggested_score = 0
WHERE recording_id = ? AND key = ?`, person, assigned, encodeVector(profile), intoMs+fromMs, id, into); err != nil {
		return Detail{}, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM speakers WHERE recording_id = ? AND key = ?", id, key); err != nil {
		return Detail{}, err
	}
	if err := recountSpeech(ctx, tx, id); err != nil {
		return Detail{}, err
	}
	if err := s.touchNames(ctx, tx, id); err != nil {
		return Detail{}, err
	}
	if err := tx.Commit(); err != nil {
		return Detail{}, err
	}
	if err := s.rematch(ctx, actor.ID); err != nil {
		return Detail{}, err
	}
	return s.Get(ctx, actor, id)
}

// People lists the member's people, by name.
func (s *Service) People(ctx context.Context, actor accounts.User) ([]Person, error) {
	rows, err := s.store.db.QueryContext(ctx, `
SELECT p.id, p.name, p.created_at, p.updated_at,
    COUNT(CASE WHEN s.assigned = 'user' AND s.profile IS NOT NULL THEN 1 END),
    COUNT(DISTINCT s.recording_id), COALESCE(SUM(s.speech_ms), 0)
FROM people p LEFT JOIN speakers s ON s.person_id = p.id
WHERE p.owner_id = ? GROUP BY p.id ORDER BY p.name`, actor.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	people := []Person{}
	for rows.Next() {
		var person Person
		var created, updated string
		if err := rows.Scan(&person.ID, &person.Name, &created, &updated, &person.Samples, &person.Recordings, &person.SpeechMs); err != nil {
			return nil, err
		}
		person.CreatedAt, person.UpdatedAt = parseTime(created), parseTime(updated)
		people = append(people, person)
	}
	return people, rows.Err()
}

func (s *Service) ownedPerson(ctx context.Context, tx *sql.Tx, actor accounts.User, id string) error {
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM people WHERE id = ? AND owner_id = ?", id, actor.ID).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) touchPerson(ctx context.Context, tx *sql.Tx, personID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE recordings SET names_revision = names_revision + 1
WHERE id IN (SELECT recording_id FROM speakers WHERE person_id = ?)`, personID)
	return err
}

// RenamePerson changes a person's name everywhere they appear.
func (s *Service) RenamePerson(ctx context.Context, actor accounts.User, id, name string) (Person, error) {
	name, err := cleanName(name)
	if err != nil {
		return Person{}, err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Person{}, err
	}
	defer tx.Rollback()
	if err := s.ownedPerson(ctx, tx, actor, id); err != nil {
		return Person{}, err
	}
	var other int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM people WHERE owner_id = ? AND name = ? AND id != ?", actor.ID, name, id).Scan(&other); err != nil {
		return Person{}, err
	}
	if other > 0 {
		return Person{}, ErrNameTaken
	}
	if _, err := tx.ExecContext(ctx, "UPDATE people SET name = ?, updated_at = ? WHERE id = ?", name, formatTime(s.now()), id); err != nil {
		return Person{}, err
	}
	if err := s.touchPerson(ctx, tx, id); err != nil {
		return Person{}, err
	}
	if err := tx.Commit(); err != nil {
		return Person{}, err
	}
	return s.person(ctx, actor, id)
}

func (s *Service) person(ctx context.Context, actor accounts.User, id string) (Person, error) {
	people, err := s.People(ctx, actor)
	if err != nil {
		return Person{}, err
	}
	for _, person := range people {
		if person.ID == id {
			return person, nil
		}
	}
	return Person{}, ErrNotFound
}

// DeletePerson forgets a person and their voiceprint; the speakers named
// after them become 发言人 N again.
func (s *Service) DeletePerson(ctx context.Context, actor accounts.User, id string) error {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.ownedPerson(ctx, tx, actor, id); err != nil {
		return err
	}
	if err := s.touchPerson(ctx, tx, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE speakers SET person_id = NULL, assigned = '', score = 0 WHERE person_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM people WHERE id = ?", id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.rematch(ctx, actor.ID)
}

// MergePeople makes two people one: the speakers named after the first are
// named after the second, whose voiceprint learns theirs.
func (s *Service) MergePeople(ctx context.Context, actor accounts.User, id, into string) (Person, error) {
	if id == into {
		return Person{}, fmt.Errorf("%w: a person merges into another", ErrInvalid)
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return Person{}, err
	}
	defer tx.Rollback()
	for _, person := range []string{id, into} {
		if err := s.ownedPerson(ctx, tx, actor, person); err != nil {
			return Person{}, err
		}
	}
	if err := s.touchPerson(ctx, tx, id); err != nil {
		return Person{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE speakers SET person_id = ? WHERE person_id = ?", into, id); err != nil {
		return Person{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE speakers SET suggested_id = ? WHERE suggested_id = ?", into, id); err != nil {
		return Person{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO rejections (recording_id, key, person_id) SELECT recording_id, key, ? FROM rejections WHERE person_id = ?", into, id); err != nil {
		return Person{}, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM people WHERE id = ?", id); err != nil {
		return Person{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE people SET updated_at = ? WHERE id = ?", formatTime(s.now()), into); err != nil {
		return Person{}, err
	}
	if err := tx.Commit(); err != nil {
		return Person{}, err
	}
	if err := s.rematch(ctx, actor.ID); err != nil {
		return Person{}, err
	}
	return s.person(ctx, actor, into)
}

// peopleNames are the member's people's names, which the recognition service
// is told to expect.
func (s *Service) peopleNames(ctx context.Context, ownerID string) []string {
	rows, err := s.store.db.QueryContext(ctx, "SELECT name FROM people WHERE owner_id = ? ORDER BY updated_at DESC LIMIT 100", ownerID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil {
			names = append(names, name)
		}
	}
	return names
}
