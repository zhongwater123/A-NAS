package photos

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"math"
	"slices"
	"strings"
	"unicode/utf8"
)

// People (docs/architecture/photo-people.md): each library keeps its own
// people, found by clustering the faces of its photos and named, merged and
// corrected by its members. Clustering follows Immich's incremental rule, with
// the product's constraints: a face joins the person of its nearest already
// placed neighbour within the threshold; with nobody near, it founds a person
// once enough unplaced faces of other photos are near it; two faces of one
// photo never share a person; and what stayed unplaced is tried again after
// each pass. It never moves a face that has a person, so whatever a member
// decided stays decided.

// PeopleCalibration is how one kind of occurrence from one model clusters:
// two occurrences are neighbours at Threshold cosine similarity or more, and
// a person needs MinCore of them from different photos to be founded. With
// Rule RuleCentroid an occurrence joins a neighbour's person only if it is
// also within Threshold of that person's mean vector, which stops a chain of
// look-alikes (one sibling to the next) from drifting into one person. The
// people evaluation chooses all three at 99% pairwise precision
// (docs/research/photo-people-evaluation.md).
type PeopleCalibration struct {
	Kind      string
	Model     string
	Threshold float64
	MinCore   int
	Rule      string
}

// Clustering rules: join the nearest placed neighbour's person, or only one
// whose mean is near too.
const (
	RuleNearest  = "nearest"
	RuleCentroid = "centroid"
)

// defaultPeopleCalibrations are the built-in calibrations. A Worker whose face
// models have none scans faces, but nobody is clustered from them.
var defaultPeopleCalibrations = []PeopleCalibration{
	// AuraFace on YuNet's faces: held at least 99.6% pairwise precision on
	// LFW, FG-NET (children growing up) and Gallagher's family album
	// (docs/research/photo-people-evaluation.md).
	{Kind: OccurrenceFace, Model: "yunet-2026may@ebafce4e3c11+auraface-v1@a7933ea53301+opencv-5.0.0+ort-1.31.0+s0.8k64",
		Threshold: 0.48, MinCore: 2, Rule: RuleCentroid},
}

// Person kinds.
const (
	PersonKindPerson = "person"
	PersonKindCat    = "cat"
	PersonKindDog    = "dog"
)

// MaxPersonNameRunes bounds a person's name.
const MaxPersonNameRunes = 40

// Person is a person or pet of one library.
type Person struct {
	ID        string `json:"id"`
	LibraryID string `json:"libraryId"`
	Kind      string `json:"kind"`
	// Name is empty until a member names the person.
	Name   string `json:"name"`
	Hidden bool   `json:"hidden"`
	// Photos counts the untrashed photos of the person.
	Photos int `json:"photos"`
	// CoverID is the occurrence whose avatar stands for the person.
	CoverID string `json:"coverId"`
	// CanChange says whether the caller may name, merge, hide or correct.
	CanChange bool `json:"canChange"`
}

// AssetPerson is one face or pet in a photo's details, with who it is.
type AssetPerson struct {
	OccurrenceID string `json:"occurrenceId"`
	Kind         string `json:"kind"`
	PersonID     string `json:"personId,omitempty"`
	Name         string `json:"name,omitempty"`
}

// PersonChange is what a member changes about a person; nil fields stay.
type PersonChange struct {
	Name    *string
	Hidden  *bool
	CoverID *string
}

var personKinds = map[string]string{
	OccurrenceFace: PersonKindPerson,
	OccurrenceCat:  PersonKindCat,
	OccurrenceDog:  PersonKindDog,
}

// canChangePeople: the owner of a private library, and every member in the
// shared library (2026-10-15 decision); an administrator who only views a
// private library cannot.
func canChangePeople(p Principal, lib library) bool {
	if !p.valid() {
		return false
	}
	return lib.kind == LibraryKindShared || lib.ownerUserID == p.UserID
}

func (s *Service) peopleCalibration(kind, model string) (PeopleCalibration, bool) {
	for _, calibration := range s.peopleCalibrations {
		if calibration.Kind == kind && calibration.Model == model {
			return calibration, true
		}
	}
	return PeopleCalibration{}, false
}

// clusterMember is one occurrence a library has placed or is placing.
type clusterMember struct {
	id       string
	objectID string
	vector   []float32
	person   string // "" while nobody
	source   string
	rejected map[string]bool
}

// clusterPeople places every face that some library has not considered yet,
// library by library, and reports whether it changed anything.
func (s *Service) clusterPeople(ctx context.Context) (bool, error) {
	s.aiModelMu.Lock()
	s.facesScanned = 0
	s.aiModelMu.Unlock()
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT a.library_id, o.kind, o.model FROM occurrences o
JOIN assets a ON a.object_id = o.object_id
WHERE NOT EXISTS (SELECT 1 FROM person_occurrences po WHERE po.library_id = a.library_id AND po.occurrence_id = o.id)
UNION
SELECT DISTINCT po.library_id, o.kind, o.model FROM person_occurrences po
JOIN occurrences o ON o.id = po.occurrence_id WHERE po.source = 'moved'`)
	if err != nil {
		return false, err
	}
	type group struct{ library, kind, model string }
	var groups []group
	for rows.Next() {
		var g group
		if err := rows.Scan(&g.library, &g.kind, &g.model); err != nil {
			_ = rows.Close()
			return false, err
		}
		groups = append(groups, g)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return false, err
	}
	changed := false
	for _, g := range groups {
		calibration, ok := s.peopleCalibration(g.kind, g.model)
		if !ok {
			continue
		}
		done, err := s.clusterLibrary(ctx, g.library, calibration)
		if err != nil {
			return changed, err
		}
		changed = changed || done
	}
	return changed, nil
}

// clusterLibrary places one library's unconsidered occurrences of one kind
// and model in the order their photos arrived, then gives every unplaced one
// a second chance.
func (s *Service) clusterLibrary(ctx context.Context, libraryID string, calibration PeopleCalibration) (bool, error) {
	members, err := s.clusterMembers(ctx, libraryID, calibration, true)
	if err != nil {
		return false, err
	}
	pending, err := s.clusterMembers(ctx, libraryID, calibration, false)
	if err != nil {
		return false, err
	}
	moved := false
	for _, m := range members {
		moved = moved || m.source == "moved"
	}
	if len(pending) == 0 && !moved {
		return false, nil
	}
	photosOf := make(map[string]map[string]bool)
	sums := make(map[string][]float64) // person -> sum of its members' vectors
	add := func(person string, vector []float32) {
		if sums[person] == nil {
			sums[person] = make([]float64, len(vector))
		}
		for k, value := range vector {
			sums[person][k] += float64(value)
		}
	}
	nearMean := func(person string, vector []float32) bool {
		sum := sums[person]
		var norm, product float64
		for k, value := range sum {
			norm += value * value
			product += value * float64(vector[k])
		}
		return norm > 0 && product/math.Sqrt(norm) >= calibration.Threshold
	}
	for _, m := range members {
		if m.person != "" {
			if photosOf[m.person] == nil {
				photosOf[m.person] = make(map[string]bool)
			}
			photosOf[m.person][m.objectID] = true
			add(m.person, m.vector)
		}
	}
	var created []string
	founded := make(map[string][]int) // person -> members that founded it
	placedSet := make(map[int]bool)

	// What moved over from earlier models stays only where the new vectors
	// agree with the person's core: the faces a member confirmed, or else
	// the densest group of the person's faces. The rest is nobody again and
	// gets a second chance below, so a better model can undo an older one's
	// merges while the name stays with the face it was given for.
	settled := make(map[int]bool)
	byPerson := make(map[string][]int)
	for i, m := range members {
		switch {
		case m.person != "":
			byPerson[m.person] = append(byPerson[m.person], i)
		case m.source == "moved":
			// Nobody before, nobody now: it only stops waiting.
			settled[i] = true
			members[i].source = "auto"
		}
	}
	for person, held := range byPerson {
		var waiting, anchors []int
		for _, i := range held {
			switch members[i].source {
			case "moved":
				waiting = append(waiting, i)
			case "user":
				anchors = append(anchors, i)
			}
		}
		if len(waiting) == 0 {
			continue
		}
		if len(anchors) == 0 {
			anchors = densestGroup(members, held, calibration.Threshold)
		}
		centre := make([]float64, len(members[held[0]].vector))
		for _, i := range anchors {
			for k, value := range members[i].vector {
				centre[k] += float64(value)
			}
		}
		for _, i := range waiting {
			settled[i] = true
			members[i].source = "auto"
			var norm, product float64
			for k, value := range centre {
				norm += value * value
				product += value * float64(members[i].vector[k])
			}
			if norm > 0 && product/math.Sqrt(norm) >= calibration.Threshold {
				continue
			}
			for k, value := range members[i].vector {
				sums[person][k] -= float64(value)
			}
			delete(photosOf[person], members[i].objectID)
			members[i].person = ""
		}
	}

	// place gives member i a person among the first seen members, if it
	// earns one: in arrival order a face sees only those before it, as the
	// evaluation measured, and the second chance sees everyone.
	place := func(i, seen int) {
		m := members[i]
		var near, placed []int
		for j, other := range members[:seen] {
			if j == i || other.objectID == m.objectID {
				continue
			}
			if dot(m.vector, other.vector) < calibration.Threshold {
				continue
			}
			near = append(near, j)
			if other.person != "" && !m.rejected[other.person] && !photosOf[other.person][m.objectID] {
				placed = append(placed, j)
			}
		}
		slices.SortFunc(placed, func(a, b int) int {
			return cmp.Compare(dot(m.vector, members[b].vector), dot(m.vector, members[a].vector))
		})
		for _, j := range placed {
			person := members[j].person
			if calibration.Rule == RuleCentroid && !nearMean(person, m.vector) {
				continue
			}
			members[i].person = person
			photosOf[person][m.objectID] = true
			add(person, m.vector)
			placedSet[i] = true
			return
		}
		if len(placed) > 0 {
			// Near people, but not near enough to any of them: nobody yet.
			return
		}
		// Found a person with the nearest unplaced neighbours of other photos.
		slices.SortFunc(near, func(a, b int) int {
			return cmp.Compare(dot(m.vector, members[b].vector), dot(m.vector, members[a].vector))
		})
		group := []int{i}
		photos := map[string]bool{m.objectID: true}
		for _, j := range near {
			if members[j].person == "" && members[j].source == "auto" && !photos[members[j].objectID] {
				group = append(group, j)
				photos[members[j].objectID] = true
			}
		}
		if len(group) < calibration.MinCore {
			return
		}
		person := s.randomID("person")
		created = append(created, person)
		founded[person] = group
		photosOf[person] = photos
		for _, j := range group {
			members[j].person = person
			add(person, members[j].vector)
			placedSet[j] = true
		}
	}

	start := len(members)
	members = append(members, pending...)
	for i := start; i < len(members); i++ {
		if members[i].person == "" {
			place(i, i)
		}
	}
	// Second chances: an unplaced face can only have become placeable next
	// to a face this pass placed or added, so only those neighbours are
	// tried, not every pair of the library again.
	retry := make(map[int]bool)
	for i := range settled {
		retry[i] = members[i].person == ""
	}
	for j := range members {
		if j < start && !placedSet[j] && !settled[j] {
			continue
		}
		for i := range members {
			if !retry[i] && members[i].person == "" && members[i].source == "auto" && i != j &&
				members[i].objectID != members[j].objectID &&
				dot(members[i].vector, members[j].vector) >= calibration.Threshold {
				retry[i] = true
			}
		}
	}
	for i := range members {
		if retry[i] && members[i].person == "" {
			place(i, len(members))
		}
	}

	err = s.withTx(ctx, func(tx *sql.Tx) error {
		now := formatTime(s.now())
		kind := personKinds[calibration.Kind]
		for _, person := range created {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO people(id, library_id, kind, created_at, updated_at) VALUES(?, ?, ?, ?, ?)",
				person, libraryID, kind, now, now); err != nil {
				return err
			}
		}
		for i, m := range members {
			var person any
			if m.person != "" {
				person = m.person
			}
			if i >= start {
				_, err := tx.ExecContext(ctx, `
INSERT INTO person_occurrences(library_id, occurrence_id, person_id, source, created_at) VALUES(?, ?, ?, 'auto', ?)
ON CONFLICT(library_id, occurrence_id) DO NOTHING`, libraryID, m.id, person, now)
				if err != nil {
					return err
				}
				continue
			}
			if settled[i] {
				if _, err := tx.ExecContext(ctx,
					"UPDATE person_occurrences SET person_id = ?, source = 'auto' WHERE library_id = ? AND occurrence_id = ? AND source = 'moved'",
					person, libraryID, m.id); err != nil {
					return err
				}
				continue
			}
			if placedSet[i] {
				// Only an unplaced face moves, so a member's decision stays.
				if _, err := tx.ExecContext(ctx,
					"UPDATE person_occurrences SET person_id = ? WHERE library_id = ? AND occurrence_id = ? AND person_id IS NULL",
					person, libraryID, m.id); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return true, err
}

// densestGroup is the member of held with the most neighbours within
// threshold among held, with those neighbours.
func densestGroup(members []clusterMember, held []int, threshold float64) []int {
	var best []int
	for _, i := range held {
		group := []int{i}
		for _, j := range held {
			if j != i && dot(members[i].vector, members[j].vector) >= threshold {
				group = append(group, j)
			}
		}
		if len(group) > len(best) {
			best = group
		}
	}
	return best
}

// clusterMembers reads a library's occurrences of one kind and model: those
// it has considered, with their people, or those it has not.
func (s *Service) clusterMembers(ctx context.Context, libraryID string, calibration PeopleCalibration, considered bool) ([]clusterMember, error) {
	query := `
SELECT o.id, o.object_id, o.vector, IFNULL(po.person_id, ''), po.source FROM person_occurrences po
JOIN occurrences o ON o.id = po.occurrence_id
WHERE po.library_id = ? AND o.kind = ? AND o.model = ?
ORDER BY po.created_at, o.object_id, o.ordinal`
	args := []any{libraryID, calibration.Kind, calibration.Model}
	if !considered {
		query = `
SELECT o.id, o.object_id, o.vector, '', 'auto' FROM occurrences o
JOIN (SELECT object_id, MIN(imported_at) AS arrived FROM assets WHERE library_id = ? GROUP BY object_id) a
  ON a.object_id = o.object_id
WHERE o.kind = ? AND o.model = ?
  AND NOT EXISTS (SELECT 1 FROM person_occurrences po WHERE po.library_id = ? AND po.occurrence_id = o.id)
ORDER BY a.arrived, o.object_id, o.ordinal`
		args = append(args, libraryID)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []clusterMember
	index := make(map[string]int)
	for rows.Next() {
		var m clusterMember
		var vector []byte
		if err := rows.Scan(&m.id, &m.objectID, &vector, &m.person, &m.source); err != nil {
			return nil, err
		}
		m.vector = decodeVector(vector)
		index[m.id] = len(members)
		members = append(members, m)
	}
	if err := rows.Err(); err != nil || !considered {
		return members, err
	}
	rejections, err := s.db.QueryContext(ctx,
		"SELECT occurrence_id, person_id FROM occurrence_rejections WHERE library_id = ?", libraryID)
	if err != nil {
		return nil, err
	}
	defer rejections.Close()
	for rejections.Next() {
		var occurrence, person string
		if err := rejections.Scan(&occurrence, &person); err != nil {
			return nil, err
		}
		if i, ok := index[occurrence]; ok {
			if members[i].rejected == nil {
				members[i].rejected = make(map[string]bool)
			}
			members[i].rejected[person] = true
		}
	}
	return members, rejections.Err()
}

// People lists the people of the libraries a search would cover: named ones
// first, then by how many photos they are in. Hidden people are left out
// unless hidden is set. A person seen in fewer than two photos is shown only
// once named.
func (s *Service) People(ctx context.Context, p Principal, viewing string, hidden bool) ([]Person, error) {
	scope, err := s.searchScope(ctx, p, viewing)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT pe.id, pe.library_id, pe.kind, pe.name, pe.hidden, IFNULL(pe.cover_occurrence_id, ''), COUNT(DISTINCT a.id),
  (SELECT o2.id FROM person_occurrences po2 JOIN occurrences o2 ON o2.id = po2.occurrence_id
   WHERE po2.person_id = pe.id AND po2.library_id = pe.library_id
     AND EXISTS (SELECT 1 FROM assets a2 WHERE a2.object_id = o2.object_id AND a2.library_id = pe.library_id AND a2.trashed_at IS NULL)
   ORDER BY o2.score DESC, o2.id LIMIT 1)
FROM people pe
JOIN person_occurrences po ON po.person_id = pe.id AND po.library_id = pe.library_id
JOIN occurrences o ON o.id = po.occurrence_id
JOIN assets a ON a.object_id = o.object_id AND a.library_id = pe.library_id AND a.trashed_at IS NULL
WHERE pe.library_id IN (`+placeholders(len(scope))+`)
GROUP BY pe.id`, scope...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	people := []Person{}
	var covers []string
	for rows.Next() {
		var person Person
		var cover, best sql.NullString
		if err := rows.Scan(&person.ID, &person.LibraryID, &person.Kind, &person.Name, &person.Hidden, &cover,
			&person.Photos, &best); err != nil {
			return nil, err
		}
		if (person.Hidden && !hidden) || (person.Name == "" && person.Photos < 2) {
			continue
		}
		person.CoverID = best.String
		people = append(people, person)
		covers = append(covers, cover.String)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	changeable := make(map[string]bool)
	for i := range people {
		if covers[i] != "" && s.coverStillTheirs(ctx, people[i].ID, covers[i]) {
			people[i].CoverID = covers[i]
		}
		can, known := changeable[people[i].LibraryID]
		if !known {
			lib, err := s.libraryByID(ctx, s.db, people[i].LibraryID)
			if err != nil {
				return nil, err
			}
			can = canChangePeople(p, lib)
			changeable[people[i].LibraryID] = can
		}
		people[i].CanChange = can
	}
	slices.SortStableFunc(people, func(a, b Person) int {
		if (a.Name == "") != (b.Name == "") {
			if a.Name == "" {
				return 1
			}
			return -1
		}
		if c := cmp.Compare(b.Photos, a.Photos); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return people, nil
}

func (s *Service) coverStillTheirs(ctx context.Context, personID, occurrenceID string) bool {
	var ok bool
	err := s.db.QueryRowContext(ctx,
		"SELECT EXISTS (SELECT 1 FROM person_occurrences WHERE person_id = ? AND occurrence_id = ?)",
		personID, occurrenceID).Scan(&ok)
	return err == nil && ok
}

func placeholders(count int) string { return strings.TrimSuffix(strings.Repeat("?,", count), ",") }

// visiblePerson returns a person p may see, with its library.
func (s *Service) visiblePerson(ctx context.Context, q queryer, p Principal, personID string) (Person, library, error) {
	if !p.valid() {
		return Person{}, library{}, ErrForbidden
	}
	var person Person
	err := q.QueryRowContext(ctx, "SELECT id, library_id, kind, name, hidden FROM people WHERE id = ?", personID).
		Scan(&person.ID, &person.LibraryID, &person.Kind, &person.Name, &person.Hidden)
	if errors.Is(err, sql.ErrNoRows) {
		return Person{}, library{}, ErrNotFound
	}
	if err != nil {
		return Person{}, library{}, err
	}
	lib, err := s.visibleLibrary(ctx, q, p, person.LibraryID)
	if err != nil {
		return Person{}, library{}, err
	}
	person.CanChange = canChangePeople(p, lib)
	return person, lib, nil
}

// PersonPhotos lists by page the untrashed photos of a person, newest first.
func (s *Service) PersonPhotos(ctx context.Context, p Principal, personID, cursor string, limit int) (Page, error) {
	person, _, err := s.visiblePerson(ctx, s.db, p, personID)
	if err != nil {
		return Page{}, err
	}
	where := `WHERE a.library_id = ? AND a.trashed_at IS NULL AND a.object_id IN (
  SELECT o.object_id FROM person_occurrences po JOIN occurrences o ON o.id = po.occurrence_id
  WHERE po.person_id = ? AND po.library_id = ?)`
	return s.newestFirst(ctx, p, where, []any{person.LibraryID, person.ID, person.LibraryID}, cursor, limit)
}

// UpdatePerson names, hides or changes the cover of a person.
func (s *Service) UpdatePerson(ctx context.Context, p Principal, personID string, change PersonChange) (Person, error) {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		person, lib, err := s.visiblePerson(ctx, tx, p, personID)
		if err != nil {
			return err
		}
		if !canChangePeople(p, lib) {
			return ErrForbidden
		}
		now := formatTime(s.now())
		if change.Name != nil {
			name, ok := cleanPersonName(*change.Name)
			if !ok {
				return ErrInvalidName
			}
			if _, err := tx.ExecContext(ctx, "UPDATE people SET name = ?, updated_at = ? WHERE id = ?", name, now, person.ID); err != nil {
				return err
			}
			if err := s.audit(ctx, tx, p.UserID, "person.renamed", person.ID, name); err != nil {
				return err
			}
		}
		if change.Hidden != nil {
			if _, err := tx.ExecContext(ctx, "UPDATE people SET hidden = ?, updated_at = ? WHERE id = ?", *change.Hidden, now, person.ID); err != nil {
				return err
			}
			action := "person.shown"
			if *change.Hidden {
				action = "person.hidden"
			}
			if err := s.audit(ctx, tx, p.UserID, action, person.ID, person.Name); err != nil {
				return err
			}
		}
		if change.CoverID != nil {
			var theirs bool
			if err := tx.QueryRowContext(ctx,
				"SELECT EXISTS (SELECT 1 FROM person_occurrences WHERE person_id = ? AND occurrence_id = ?)",
				person.ID, *change.CoverID).Scan(&theirs); err != nil {
				return err
			}
			if !theirs {
				return ErrConflict
			}
			if _, err := tx.ExecContext(ctx, "UPDATE people SET cover_occurrence_id = ?, updated_at = ? WHERE id = ?",
				*change.CoverID, now, person.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Person{}, err
	}
	return s.personSummary(ctx, p, personID)
}

// personSummary is a person as People lists it, hidden or not.
func (s *Service) personSummary(ctx context.Context, p Principal, personID string) (Person, error) {
	person, lib, err := s.visiblePerson(ctx, s.db, p, personID)
	if err != nil {
		return Person{}, err
	}
	viewing := ""
	if viewingOnly(p, lib) {
		viewing = lib.id
	}
	all, err := s.People(ctx, p, viewing, true)
	if err != nil {
		return Person{}, err
	}
	for _, listed := range all {
		if listed.ID == personID {
			return listed, nil
		}
	}
	// Named but without photos left, or seen once.
	return person, nil
}

func cleanPersonName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		// Clearing the name is allowed.
		return "", true
	}
	cleaned, ok := cleanName(name)
	return cleaned, ok && utf8.RuneCountInString(cleaned) <= MaxPersonNameRunes
}

// MergePeople moves every face of a person into another of the same library
// and kind, which keeps its name, or takes the merged person's name when it
// has none. The merged person is gone afterwards.
func (s *Service) MergePeople(ctx context.Context, p Principal, personID, intoID string) (Person, error) {
	if personID == intoID {
		return Person{}, ErrConflict
	}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		from, lib, err := s.visiblePerson(ctx, tx, p, personID)
		if err != nil {
			return err
		}
		into, _, err := s.visiblePerson(ctx, tx, p, intoID)
		if err != nil {
			return err
		}
		if !canChangePeople(p, lib) {
			return ErrForbidden
		}
		if from.LibraryID != into.LibraryID || from.Kind != into.Kind {
			return ErrConflict
		}
		now := formatTime(s.now())
		// One photo cannot hold the same person twice: the face that would
		// collide stays with nobody.
		if _, err := tx.ExecContext(ctx, `
UPDATE person_occurrences SET person_id = NULL WHERE person_id = ? AND occurrence_id IN (
  SELECT po.occurrence_id FROM person_occurrences po JOIN occurrences o ON o.id = po.occurrence_id
  WHERE po.person_id = ? AND o.object_id IN (
    SELECT o2.object_id FROM person_occurrences po2 JOIN occurrences o2 ON o2.id = po2.occurrence_id WHERE po2.person_id = ?))`,
			from.ID, from.ID, into.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE person_occurrences SET person_id = ?, source = 'user' WHERE person_id = ?", into.ID, from.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO occurrence_rejections(library_id, occurrence_id, person_id, created_by, created_at)
SELECT library_id, occurrence_id, ?, created_by, created_at FROM occurrence_rejections WHERE person_id = ?
ON CONFLICT DO NOTHING`, into.ID, from.ID); err != nil {
			return err
		}
		if into.Name == "" && from.Name != "" {
			if _, err := tx.ExecContext(ctx, "UPDATE people SET name = ? WHERE id = ?", from.Name, into.ID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE people SET updated_at = ? WHERE id = ?", now, into.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM people WHERE id = ?", from.ID); err != nil {
			return err
		}
		return s.audit(ctx, tx, p.UserID, "person.merged", into.ID, from.ID)
	})
	if err != nil {
		return Person{}, err
	}
	return s.personSummary(ctx, p, intoID)
}

// RemoveFromPerson takes a photo out of a person: the photo's faces that the
// person held belong to nobody, and clustering never gives them back to that
// person; it may still find them someone else.
func (s *Service) RemoveFromPerson(ctx context.Context, p Principal, personID, assetID string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		person, lib, err := s.visiblePerson(ctx, tx, p, personID)
		if err != nil {
			return err
		}
		record, err := s.visibleAsset(ctx, tx, p, assetID)
		if err != nil {
			return err
		}
		if !canChangePeople(p, lib) {
			return ErrForbidden
		}
		if record.library.id != person.LibraryID {
			return ErrNotFound
		}
		rows, err := tx.QueryContext(ctx, `
SELECT po.occurrence_id FROM person_occurrences po JOIN occurrences o ON o.id = po.occurrence_id
WHERE po.person_id = ? AND po.library_id = ? AND o.object_id = ?`, person.ID, person.LibraryID, record.objectID)
		if err != nil {
			return err
		}
		var occurrences []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			occurrences = append(occurrences, id)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		if len(occurrences) == 0 {
			return ErrNotFound
		}
		now := formatTime(s.now())
		for _, occurrence := range occurrences {
			if _, err := tx.ExecContext(ctx,
				"UPDATE person_occurrences SET person_id = NULL, source = 'auto' WHERE library_id = ? AND occurrence_id = ?",
				person.LibraryID, occurrence); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
INSERT INTO occurrence_rejections(library_id, occurrence_id, person_id, created_by, created_at) VALUES(?, ?, ?, ?, ?)
ON CONFLICT DO NOTHING`, person.LibraryID, occurrence, person.ID, p.UserID, now); err != nil {
				return err
			}
		}
		return s.audit(ctx, tx, p.UserID, "person.photo_removed", person.ID, record.ID)
	})
}

// AssignOccurrence says who a face is: an existing person of the photo's
// library and kind, or a new person with the given name. It is a member's
// decision, which clustering never changes.
func (s *Service) AssignOccurrence(ctx context.Context, p Principal, assetID, occurrenceID, personID, newName string) (Asset, error) {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		record, err := s.visibleAsset(ctx, tx, p, assetID)
		if err != nil {
			return err
		}
		if !canChangePeople(p, record.library) {
			return ErrForbidden
		}
		var kind string
		if err := tx.QueryRowContext(ctx, "SELECT kind FROM occurrences WHERE id = ? AND object_id = ?",
			occurrenceID, record.objectID).Scan(&kind); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		now := formatTime(s.now())
		if personID == "" {
			name, ok := cleanPersonName(newName)
			if !ok || name == "" {
				return ErrInvalidName
			}
			personID = s.randomID("person")
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO people(id, library_id, kind, name, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?)",
				personID, record.library.id, personKinds[kind], name, now, now); err != nil {
				return err
			}
		} else {
			person, _, err := s.visiblePerson(ctx, tx, p, personID)
			if err != nil {
				return err
			}
			if person.LibraryID != record.library.id || person.Kind != personKinds[kind] {
				return ErrConflict
			}
		}
		// The photo's other faces leave this person: one photo, one face each.
		if _, err := tx.ExecContext(ctx, `
UPDATE person_occurrences SET person_id = NULL WHERE library_id = ? AND person_id = ? AND occurrence_id IN (
  SELECT id FROM occurrences WHERE object_id = ? AND id <> ?)`, record.library.id, personID, record.objectID, occurrenceID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO person_occurrences(library_id, occurrence_id, person_id, source, created_at) VALUES(?, ?, ?, 'user', ?)
ON CONFLICT(library_id, occurrence_id) DO UPDATE SET person_id = excluded.person_id, source = 'user'`,
			record.library.id, occurrenceID, personID, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM occurrence_rejections WHERE library_id = ? AND occurrence_id = ? AND person_id = ?",
			record.library.id, occurrenceID, personID); err != nil {
			return err
		}
		return s.audit(ctx, tx, p.UserID, "person.face_assigned", personID, record.ID)
	})
	if err != nil {
		return Asset{}, err
	}
	record, err := s.visibleAsset(ctx, s.db, p, assetID)
	if err != nil {
		return Asset{}, err
	}
	return s.details(ctx, p, record)
}

// peopleOf lists the faces and pets in a photo with who each is in the
// photo's library.
func (s *Service) peopleOf(ctx context.Context, record assetRecord) ([]AssetPerson, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT o.id, o.kind, IFNULL(pe.id, ''), IFNULL(pe.name, '') FROM occurrences o
LEFT JOIN person_occurrences po ON po.occurrence_id = o.id AND po.library_id = ?
LEFT JOIN people pe ON pe.id = po.person_id
WHERE o.object_id = ? ORDER BY o.derivation, o.ordinal`, record.library.id, record.objectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found []AssetPerson
	for rows.Next() {
		var person AssetPerson
		if err := rows.Scan(&person.OccurrenceID, &person.Kind, &person.PersonID, &person.Name); err != nil {
			return nil, err
		}
		found = append(found, person)
	}
	return found, rows.Err()
}

// OccurrenceAvatar returns the avatar of a face or pet in a photo p can see.
func (s *Service) OccurrenceAvatar(ctx context.Context, p Principal, occurrenceID string) ([]byte, error) {
	if !p.valid() {
		return nil, ErrForbidden
	}
	var objectID string
	var ordinal int
	err := s.db.QueryRowContext(ctx, "SELECT object_id, ordinal FROM occurrences WHERE id = ? AND ordinal >= 0", occurrenceID).
		Scan(&objectID, &ordinal)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	records, err := s.queryAssets(ctx, s.db, "WHERE a.object_id = ?", objectID)
	if err != nil {
		return nil, err
	}
	visible := false
	for _, record := range records {
		if canView(p, record.library, s.now()) && (record.Trash == nil || canChange(p, record.library, record.UploadedBy)) {
			visible = true
			break
		}
	}
	if !visible {
		return nil, ErrNotFound
	}
	avatar, err := s.avatarTile(objectID, ordinal)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return avatar, err
}
