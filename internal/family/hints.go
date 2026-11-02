package family

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"unicode/utf8"
)

// Hint is something the desktop tells a member once after they sign in: a
// vaccine dose that came due in the last hintBacklogDays or comes up within
// soonDays, or, for guardians, someone asking to join.
type Hint struct {
	Key       string `json:"key"`
	Kind      string `json:"kind"`
	BabyID    string `json:"babyId,omitempty"`
	BabyName  string `json:"babyName,omitempty"`
	Vaccine   string `json:"vaccine,omitempty"`
	Dose      int    `json:"dose,omitempty"`
	DueDate   string `json:"dueDate,omitempty"`
	AccountID string `json:"accountId,omitempty"`
	Username  string `json:"username,omitempty"`
	Role      string `json:"role,omitempty"`
}

const (
	HintVaccine     = "vaccine"
	HintJoinRequest = "join_request"
	maxHintKeys     = 200
	maxHintKeyBytes = 200
	// hintBacklogDays bounds how long ago a due dose is still news: a dose due
	// earlier was most likely given and not recorded, as for a child added
	// at three. The board lists it; the desktop does not.
	hintBacklogDays = 60
)

// Hints lists what the caller has not been told yet. Accounts outside the
// family have nothing to be told.
func (s *Store) Hints(ctx context.Context, actor Actor) ([]Hint, error) {
	hints := []Hint{}
	_, me, err := s.requireMember(ctx, s.db, actor)
	if errors.Is(err, ErrNotMember) {
		return hints, nil
	}
	if err != nil {
		return nil, err
	}
	seen, err := s.seenHints(ctx, actor)
	if err != nil {
		return nil, err
	}
	babies, err := s.babies(ctx, s.db, false)
	if err != nil {
		return nil, err
	}
	today := s.today()
	for _, baby := range babies {
		rows, err := s.queryRows(ctx, s.db, "SELECT "+recordColumns+
			" FROM records WHERE baby_id = ? AND kind = 'vaccine' AND deleted_at IS NULL ORDER BY occurred_on LIMIT 1000", baby.ID)
		if err != nil {
			return nil, err
		}
		records := make([]Record, 0, len(rows))
		for _, r := range rows {
			records = append(records, r.record)
		}
		plan, err := s.vaccinePlan(ctx, s.db, baby.ID)
		if err != nil {
			return nil, err
		}
		birth, _ := parseDate(baby.BirthDate)
		for _, dose := range schedule(birth, baby.Sex, today, plan, records) {
			if dose.Status != DoseAvailable && dose.Status != DoseSoon {
				continue
			}
			if due, _ := parseDate(dose.DueDate); due.Before(today.AddDate(0, 0, -hintBacklogDays)) {
				continue
			}
			key := fmt.Sprintf("vaccine:%s:%s:%d", baby.ID, dose.Code, dose.Dose)
			if seen[key] {
				continue
			}
			hints = append(hints, Hint{Key: key, Kind: HintVaccine, BabyID: baby.ID, BabyName: baby.Name,
				Vaccine: dose.Name, Dose: dose.Dose, DueDate: dose.DueDate})
		}
	}
	if me.Guardian {
		requests, err := s.joinRequests(ctx, s.db)
		if err != nil {
			return nil, err
		}
		for _, request := range requests {
			key := "join:" + request.AccountID + ":" + formatTime(request.RequestedAt)
			if seen[key] {
				continue
			}
			hints = append(hints, Hint{Key: key, Kind: HintJoinRequest, AccountID: request.AccountID,
				Username: request.Username, Role: request.Role})
		}
	}
	return hints, nil
}

func (s *Store) seenHints(ctx context.Context, actor Actor) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT hint_key FROM hints_seen WHERE account_id = ?", actor.AccountID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	seen := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		seen[key] = true
	}
	return seen, rows.Err()
}

// MarkHintsSeen records that the caller was told, so the desktop does not
// tell them again.
func (s *Store) MarkHintsSeen(ctx context.Context, actor Actor, keys []string) error {
	if len(keys) > maxHintKeys {
		return ErrInvalid
	}
	for _, key := range keys {
		if key == "" || len(key) > maxHintKeyBytes || !utf8.ValidString(key) {
			return ErrInvalid
		}
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, _, err := s.requireMember(ctx, tx, actor); err != nil {
			return err
		}
		now := formatTime(s.now())
		for _, key := range keys {
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO hints_seen(account_id, hint_key, seen_at) VALUES(?, ?, ?)",
				actor.AccountID, key, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// Catalog is what pages offer when adding things: the preset roles, the
// suggested milestones, the vaccine catalog and the growth standard.
type Catalog struct {
	Roles          []string       `json:"roles"`
	Milestones     []string       `json:"milestones"`
	Vaccines       VaccineCatalog `json:"vaccines"`
	GrowthStandard string         `json:"growthStandard"`
}

// CurrentCatalog returns the catalog this program ships.
func CurrentCatalog() Catalog {
	return Catalog{Roles: PresetRoles, Milestones: SuggestedMilestones, Vaccines: vaccineCatalog, GrowthStandard: growthStandard.Name}
}
