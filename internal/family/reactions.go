package family

import (
	"context"
	"database/sql"
	"errors"
	"slices"
)

// ReactionNames are the reactions a member leaves on a record with one tap,
// in the order the window shows them: ❤️ 👍 😂 🥰 👏.
var ReactionNames = []string{"heart", "like", "laugh", "adore", "clap"}

// Reaction is one kind of reaction on a record and the members who left
// it, earliest first.
type Reaction struct {
	Name       string   `json:"name"`
	AccountIDs []string `json:"accountIds"`
}

// reactionsFor returns the reactions on rows' records.
func (s *Store) reactionsFor(ctx context.Context, q queryer, rows []*row) (map[string][]Reaction, error) {
	found := map[string][]Reaction{}
	if len(rows) == 0 {
		return found, nil
	}
	ids := make([]any, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.record.ID)
	}
	result, err := q.QueryContext(ctx, "SELECT record_id, reaction, account_id FROM reactions WHERE record_id IN ("+
		placeholders(len(ids))+") ORDER BY created_at, account_id", ids...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = result.Close() }()
	who := map[string]map[string][]string{}
	for result.Next() {
		var recordID, name, accountID string
		if err := result.Scan(&recordID, &name, &accountID); err != nil {
			return nil, err
		}
		if who[recordID] == nil {
			who[recordID] = map[string][]string{}
		}
		who[recordID][name] = append(who[recordID][name], accountID)
	}
	if err := result.Err(); err != nil {
		return nil, err
	}
	for recordID, byName := range who {
		for _, name := range ReactionNames {
			if accounts := byName[name]; len(accounts) > 0 {
				found[recordID] = append(found[recordID], Reaction{Name: name, AccountIDs: accounts})
			}
		}
	}
	return found, nil
}

// React leaves actor's reaction on a record, or takes it back when on is
// false, and returns the record's reactions. Leaving one twice, or taking
// back one never left, changes nothing.
func (s *Store) React(ctx context.Context, actor Actor, recordID, name string, on bool) ([]Reaction, error) {
	if !slices.Contains(ReactionNames, name) {
		return nil, ErrInvalid
	}
	reactions := []Reaction{}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, _, err := s.requireMember(ctx, tx, actor); err != nil {
			return err
		}
		var babyID string
		err := tx.QueryRowContext(ctx, `SELECT r.baby_id FROM records r JOIN babies b ON b.id = r.baby_id
WHERE r.id = ? AND r.deleted_at IS NULL AND b.deleted_at IS NULL`, recordID).Scan(&babyID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if on {
			_, err = tx.ExecContext(ctx, `INSERT INTO reactions(record_id, account_id, reaction, created_at)
VALUES(?, ?, ?, ?) ON CONFLICT DO NOTHING`, recordID, actor.AccountID, name, formatTime(s.now()))
		} else {
			_, err = tx.ExecContext(ctx, "DELETE FROM reactions WHERE record_id = ? AND account_id = ? AND reaction = ?",
				recordID, actor.AccountID, name)
		}
		if err != nil {
			return err
		}
		found, err := s.reactionsFor(ctx, tx, []*row{{record: Record{ID: recordID}}})
		if err != nil {
			return err
		}
		if found[recordID] != nil {
			reactions = found[recordID]
		}
		return nil
	})
	return reactions, err
}
