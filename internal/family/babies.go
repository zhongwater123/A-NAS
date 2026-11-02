package family

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/avatar"
)

const maxNameRunes = 20

// Baby is a baby's profile: a family member without an account, whom the
// child's own account can claim later (ADR 0029).
type Baby struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Nickname  string `json:"nickname"`
	Sex       string `json:"sex"`
	BirthDate string `json:"birthDate"`
	// Age is the baby's age today.
	Age Age `json:"age"`
	// AvatarVersion changes with every new avatar, so pages can cache the
	// picture by it; zero means there is none.
	AvatarVersion int64      `json:"avatarVersion"`
	Version       int64      `json:"version"`
	CreatedAt     time.Time  `json:"createdAt"`
	CreatedBy     string     `json:"createdBy"`
	DeletedAt     *time.Time `json:"deletedAt,omitempty"`
}

// BabyFields is what a guardian fills in. Version is the version they read
// when editing.
type BabyFields struct {
	Name      string `json:"name"`
	Nickname  string `json:"nickname"`
	Sex       string `json:"sex"`
	BirthDate string `json:"birthDate"`
	Version   int64  `json:"version"`
}

func cleanName(name string, required bool) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", !required
	}
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxNameRunes {
		return "", false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return name, true
}

func (s *Store) cleanBaby(fields BabyFields) (BabyFields, time.Time, bool) {
	var ok bool
	if fields.Name, ok = cleanName(fields.Name, true); !ok {
		return fields, time.Time{}, false
	}
	if fields.Nickname, ok = cleanName(fields.Nickname, false); !ok {
		return fields, time.Time{}, false
	}
	if fields.Sex != "female" && fields.Sex != "male" {
		return fields, time.Time{}, false
	}
	birth, ok := parseDate(fields.BirthDate)
	if !ok || birth.After(s.today()) {
		return fields, time.Time{}, false
	}
	return fields, birth, true
}

const babyColumns = "id, name, nickname, sex, birth_date, avatar_version, version, created_at, created_by, deleted_at"

func (s *Store) scanBaby(row interface{ Scan(...any) error }) (Baby, error) {
	var baby Baby
	var created string
	var deleted sql.NullString
	if err := row.Scan(&baby.ID, &baby.Name, &baby.Nickname, &baby.Sex, &baby.BirthDate,
		&baby.AvatarVersion, &baby.Version, &created, &baby.CreatedBy, &deleted); err != nil {
		return Baby{}, err
	}
	baby.CreatedAt = parseTime(created)
	baby.DeletedAt = parseOptionalTime(deleted)
	if birth, ok := parseDate(baby.BirthDate); ok {
		baby.Age, _ = ageOn(birth, s.today())
	}
	return baby, nil
}

// babies lists the family's babies, oldest first, or those still in the
// trash.
func (s *Store) babies(ctx context.Context, q queryer, deleted bool) ([]Baby, error) {
	query := "SELECT " + babyColumns + " FROM babies WHERE deleted_at IS NULL ORDER BY birth_date, created_at"
	args := []any{}
	if deleted {
		query = "SELECT " + babyColumns + " FROM babies WHERE deleted_at IS NOT NULL AND deleted_at > ? ORDER BY deleted_at DESC"
		args = append(args, s.trashCutoff())
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	babies := []Baby{}
	for rows.Next() {
		baby, err := s.scanBaby(rows)
		if err != nil {
			return nil, err
		}
		babies = append(babies, baby)
	}
	return babies, rows.Err()
}

// baby reads a baby that is not in the trash.
func (s *Store) baby(ctx context.Context, q queryer, id string) (Baby, error) {
	baby, err := s.scanBaby(q.QueryRowContext(ctx, "SELECT "+babyColumns+" FROM babies WHERE id = ? AND deleted_at IS NULL", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Baby{}, ErrNotFound
	}
	return baby, err
}

// Baby returns one baby's profile to a member.
func (s *Store) Baby(ctx context.Context, actor Actor, id string) (Baby, error) {
	if _, _, err := s.requireMember(ctx, s.db, actor); err != nil {
		return Baby{}, err
	}
	return s.baby(ctx, s.db, id)
}

// AddBaby adds a baby and so a new channel. Only guardians add babies.
func (s *Store) AddBaby(ctx context.Context, actor Actor, fields BabyFields) (Baby, error) {
	fields, _, ok := s.cleanBaby(fields)
	if !ok {
		return Baby{}, ErrInvalid
	}
	var baby Baby
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, _, err := s.requireGuardian(ctx, tx, actor); err != nil {
			return err
		}
		id := newID("baby")
		if _, err := tx.ExecContext(ctx, `INSERT INTO babies(id, name, nickname, sex, birth_date, created_at, created_by)
VALUES(?, ?, ?, ?, ?, ?, ?)`, id, fields.Name, fields.Nickname, fields.Sex, fields.BirthDate, formatTime(s.now()), actor.AccountID); err != nil {
			return err
		}
		var err error
		if baby, err = s.baby(ctx, tx, id); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "family.baby_added", id, fields.Name)
	})
	return baby, err
}

// UpdateBaby changes a baby's profile. A guardian editing from a stale copy
// gets ErrChanged instead of overwriting someone else's change.
func (s *Store) UpdateBaby(ctx context.Context, actor Actor, id string, fields BabyFields) (Baby, error) {
	fields, birth, ok := s.cleanBaby(fields)
	if !ok {
		return Baby{}, ErrInvalid
	}
	var baby Baby
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, _, err := s.requireGuardian(ctx, tx, actor); err != nil {
			return err
		}
		current, err := s.baby(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.Version != fields.Version {
			return ErrChanged
		}
		// Records dated before the new birth date would fall outside the
		// baby's life; ask the guardian to fix them first.
		var earlier int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM records
WHERE baby_id = ? AND deleted_at IS NULL AND kind IN ('measurement', 'vaccine', 'milestone') AND occurred_on < ?`,
			id, birth.Format(dateLayout)).Scan(&earlier); err != nil {
			return err
		}
		if earlier > 0 {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `UPDATE babies SET name = ?, nickname = ?, sex = ?, birth_date = ?, version = version + 1
WHERE id = ?`, fields.Name, fields.Nickname, fields.Sex, fields.BirthDate, id); err != nil {
			return err
		}
		if baby, err = s.baby(ctx, tx, id); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "family.baby_updated", id, fields.Name)
	})
	return baby, err
}

// SetBabyAvatar stores picture, cropped and shrunk like an account avatar.
func (s *Store) SetBabyAvatar(ctx context.Context, actor Actor, id string, picture []byte) (Baby, error) {
	normalized, err := avatar.Normalize(picture)
	if err != nil {
		return Baby{}, err
	}
	return s.changeAvatar(ctx, actor, id, normalized)
}

// RemoveBabyAvatar goes back to the initial-letter placeholder.
func (s *Store) RemoveBabyAvatar(ctx context.Context, actor Actor, id string) (Baby, error) {
	return s.changeAvatar(ctx, actor, id, nil)
}

func (s *Store) changeAvatar(ctx context.Context, actor Actor, id string, picture []byte) (Baby, error) {
	var baby Baby
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, _, err := s.requireGuardian(ctx, tx, actor); err != nil {
			return err
		}
		if _, err := s.baby(ctx, tx, id); err != nil {
			return err
		}
		version := s.now().UnixMilli()
		if picture == nil {
			version = 0
		}
		if _, err := tx.ExecContext(ctx, "UPDATE babies SET avatar = ?, avatar_version = ? WHERE id = ?", picture, version, id); err != nil {
			return err
		}
		var err error
		baby, err = s.baby(ctx, tx, id)
		return err
	})
	return baby, err
}

// BabyAvatar returns the stored JPEG, or ErrNotFound when there is none.
func (s *Store) BabyAvatar(ctx context.Context, actor Actor, id string) ([]byte, error) {
	if _, _, err := s.requireMember(ctx, s.db, actor); err != nil {
		return nil, err
	}
	var picture []byte
	err := s.db.QueryRowContext(ctx, "SELECT avatar FROM babies WHERE id = ? AND deleted_at IS NULL", id).Scan(&picture)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && len(picture) == 0) {
		return nil, ErrNotFound
	}
	return picture, err
}

// DeleteBaby moves a baby and the channel into the trash for TrashDays.
// confirmName must repeat the baby's name, as the guardian typed it.
func (s *Store) DeleteBaby(ctx context.Context, actor Actor, id, confirmName string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, _, err := s.requireGuardian(ctx, tx, actor); err != nil {
			return err
		}
		baby, err := s.baby(ctx, tx, id)
		if err != nil {
			return err
		}
		if strings.TrimSpace(confirmName) != baby.Name {
			return ErrInvalid
		}
		if _, err := tx.ExecContext(ctx, "UPDATE babies SET deleted_at = ?, deleted_by = ? WHERE id = ?",
			formatTime(s.now()), actor.AccountID, id); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "family.baby_deleted", id, baby.Name)
	})
}

// RestoreBaby brings a deleted baby and the channel back.
func (s *Store) RestoreBaby(ctx context.Context, actor Actor, id string) (Baby, error) {
	var baby Baby
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, _, err := s.requireGuardian(ctx, tx, actor); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "UPDATE babies SET deleted_at = NULL, deleted_by = '' WHERE id = ? AND deleted_at > ?",
			id, s.trashCutoff())
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if baby, err = s.baby(ctx, tx, id); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "family.baby_restored", id, baby.Name)
	})
	return baby, err
}
