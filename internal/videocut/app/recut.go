package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/videocut/db"
	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
	"github.com/zhongwater123/A-NAS/internal/videocutcompute"
)

// Recutting a version from a sentence (#596, ADR 0039 decision 7): the
// member's VNAS plans the version again, one 方案, starting from the plan
// it was cut by and changing only what the sentence asks. It is the one
// part of editing that plans, so it takes a round of the planning grant.

// RecutVersion starts a recut job of owner's version. The videos, song,
// excerpt and order are the version's job's; a version edited by hand is
// planned again from the plan it came from, without the shots changed by
// hand. The same requestKey again returns the same job; a project with a
// job in progress answers ErrJobActive.
func (s *Store) RecutVersion(ctx context.Context, owner, versionID, requestKey, change string) (domain.Job, bool, error) {
	if !printable(requestKey, MaxRequestKey) || strings.TrimSpace(change) == "" || !validText(change, MaxAskRunes) {
		return domain.Job{}, false, domain.ErrInvalid
	}
	if !s.runsJobs() {
		return domain.Job{}, false, domain.ErrJobsUnavailable
	}
	jobID, err := keyedJob(ctx, s.db, owner, requestKey, KindRecut, versionID)
	if err != nil {
		return domain.Job{}, false, err
	}
	if jobID != "" {
		job, err := s.Job(ctx, owner, jobID)
		return job, false, err
	}
	src, err := s.versionSource(ctx, owner, versionID)
	if err != nil {
		return domain.Job{}, false, err
	}
	base, places, err := recutBase(src)
	if err != nil {
		return domain.Job{}, false, err
	}
	created := false
	err = s.db.Update(ctx, func(tx *db.Tx) error {
		// A retry with the same key may have made the job since the look above.
		var err error
		if jobID, err = keyedJob(ctx, tx, owner, requestKey, KindRecut, versionID); err != nil || jobID != "" {
			return err
		}
		row, err := s.project(ctx, tx, owner, src.projectID)
		if err != nil {
			return err
		}
		if err := projectIdle(ctx, tx, src.projectID); err != nil {
			return err
		}
		settings, err := jobSettings(ctx, tx, src.planJob)
		if err != nil {
			return err
		}
		settings.ProjectName, settings.Edit = row.Name, nil
		settings.Recut = &domain.RecutSettings{Change: change, Base: base, Cards: places}
		if jobID, err = s.queueJob(ctx, tx, settings, db.NewJob{Owner: owner, ProjectID: src.projectID, RequestKey: requestKey, Kind: KindRecut, Plans: 1,
			SourceVersion: sql.NullString{String: versionID, Valid: true}}); err != nil {
			return err
		}
		created = true
		// The videos the version was planned from, as they were then.
		return tx.CopyJobAssets(ctx, jobID, src.planJob)
	})
	if err != nil {
		return domain.Job{}, false, err
	}
	s.wakeJobs()
	job, err := s.Job(ctx, owner, jobID)
	return job, created, err
}

// recutBase is the plan a recut starts from: the version's outline and, for
// each part, its pictures as the second step answered them. Both are passed
// on whole, with each part's and picture's link, which the template asks to
// keep where the sentence does not touch; only what the earlier request did
// not find (unmet) is left out, which the new round says for itself. The
// plan names cards by their numbers then; places are where those cards were
// in their videos, by which the recut finds them again (alignBase).
func recutBase(src *editSource) (json.RawMessage, map[string]domain.CardPlace, error) {
	var outline map[string]any
	var shots struct {
		Answer struct {
			Parts []map[string]any `json:"parts"`
		} `json:"answer"`
	}
	if err := errors.Join(json.Unmarshal(src.pickedRaw, &outline), json.Unmarshal(src.plannedRaw, &shots)); err != nil {
		return nil, nil, domain.ErrNotEditable
	}
	delete(outline, "unmet")
	base := recutPlan{Outline: outline, Shots: shots.Answer.Parts}
	places := map[string]domain.CardPlace{}
	for _, id := range base.cards() {
		if card, ok := src.cards[id]; ok {
			places[id] = placeOf(card)
		}
	}
	encoded, err := json.Marshal(base)
	return encoded, places, err
}

func placeOf(card videocutcompute.CatalogCard) domain.CardPlace {
	return domain.CardPlace{Video: card.Video, Start: card.Start, End: card.End, Times: card.Times}
}

// recutPlan is a recut's base as it is passed on.
type recutPlan struct {
	Outline map[string]any   `json:"outline"`
	Shots   []map[string]any `json:"shots"`
}

// cards are the cards the plan names, by its scenes' cards and its pictures'
// beats.
func (plan recutPlan) cards() []string {
	var named []string
	forEachIn(plan.Outline["parts"], "scenes", func(scene map[string]any) {
		list, _ := scene["cards"].([]any)
		for _, item := range list {
			if id, ok := item.(string); ok {
				named = append(named, id)
			}
		}
	})
	for _, part := range plan.Shots {
		forEachIn(part["pictures"], "", func(picture map[string]any) {
			list, _ := picture["beats"].([]any)
			for _, item := range list {
				if beat, ok := item.(string); ok {
					named = append(named, domain.CardOf(beat))
				}
			}
		})
	}
	return named
}

// rename passes each card the plan names, by its scenes' cards and its
// pictures' beats, through to: a card it answers false for is left out, and
// a scene or picture left with none says none, so that the round picks them
// again by what it asks for (plan-v3.md).
func (plan recutPlan) rename(to func(card string) (string, bool)) {
	forEachIn(plan.Outline["parts"], "scenes", func(scene map[string]any) {
		renameIn(scene, "cards", to)
	})
	for _, part := range plan.Shots {
		forEachIn(part["pictures"], "", func(picture map[string]any) {
			renameIn(picture, "beats", func(beat string) (string, bool) {
				card, n, ok := domain.SplitBeat(beat)
				if !ok {
					return "", false
				}
				card, ok = to(card)
				return domain.BeatID(card, n), ok
			})
		})
	}
}

func renameIn(object map[string]any, field string, to func(string) (string, bool)) {
	list, ok := object[field].([]any)
	if !ok {
		return
	}
	kept := []any{}
	for _, item := range list {
		if name, ok := item.(string); ok {
			if renamed, ok := to(name); ok {
				kept = append(kept, renamed)
			}
		}
	}
	if len(kept) == 0 {
		delete(object, field)
		return
	}
	object[field] = kept
}

// alignBase is the recut's base for the cards it plans from now: each card
// the version's plan named, by the number of the card now in its place. The
// job's videos may have been taken out, or analysed again, since the version
// was cut: the cards are numbered anew, and a number of then names other
// footage now. A card no longer in its place (its video gone, or cut or
// described anew) is left out, and its scenes and pictures are picked again;
// the rest stay as they were planned.
func alignBase(base json.RawMessage, places map[string]domain.CardPlace, cards []videocutcompute.CatalogCard) (json.RawMessage, error) {
	now := map[string]string{}
	for _, card := range cards {
		now[placeOf(card).Key()] = card.ID
	}
	var plan recutPlan
	if err := json.Unmarshal(base, &plan); err != nil {
		return nil, err
	}
	plan.rename(func(card string) (string, bool) {
		place, ok := places[card]
		if !ok {
			return "", false
		}
		id, ok := now[place.Key()]
		return id, ok
	})
	return json.Marshal(plan)
}

// forEachIn calls do with each object of list, a decoded JSON array; with a
// field, each object of that array field of each of list's objects instead.
func forEachIn(list any, field string, do func(map[string]any)) {
	items, _ := list.([]any)
	for _, item := range items {
		object, ok := item.(map[string]any)
		switch {
		case !ok:
		case field != "":
			forEachIn(object[field], "", do)
		default:
			do(object)
		}
	}
}
