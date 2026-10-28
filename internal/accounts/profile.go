package accounts

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

var (
	ErrInvalidNickname = errors.New("invalid nickname")
	ErrNicknameTooLong = fmt.Errorf("%w: it must contain at most %d characters", ErrInvalidNickname, MaxNicknameLength)
	// ErrNicknameCharacters refuses control and invisible formatting
	// characters, which could make one name look like another.
	ErrNicknameCharacters = fmt.Errorf("%w: it must not contain control or invisible characters", ErrInvalidNickname)
	// ErrNicknameUnavailable reports a nickname another account already uses
	// as its username or nickname.
	ErrNicknameUnavailable = errors.New("nickname is used by another account")
	ErrAvatarNotFound      = errors.New("avatar not found")
)

// MaxNicknameLength counts Unicode code points.
const MaxNicknameLength = 24

// zeroWidthJoiner is the one invisible character a nickname may hold: emoji
// such as families and professions are joined with it.
const zeroWidthJoiner = '‍'

// userColumns are the users columns scanUser reads, in order.
const userColumns = "id, username, role, status, created_at, must_change_password, nickname, avatar_version"

type rowScanner interface{ Scan(...any) error }

func scanUser(row rowScanner) (User, error) {
	var user User
	var createdAt string
	if err := row.Scan(&user.ID, &user.Username, &user.Role, &user.Status, &createdAt, &user.MustChangePassword,
		&user.Nickname, &user.AvatarVersion); err != nil {
		return User{}, err
	}
	user.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return user, nil
}

// Avatar is a stored avatar picture.
type Avatar struct {
	Version     string
	ContentType string
	Image       []byte
	UpdatedAt   time.Time
}

// Profiles lists every account, disabled ones included because their names
// stay on what they wrote, for any signed-in user to show names and avatars.
func (s *Service) Profiles(ctx context.Context, actor User) ([]Profile, error) {
	if actor.Status != UserStatusActive {
		return nil, ErrForbidden
	}
	rows, err := s.store.db.QueryContext(ctx, "SELECT id, username, nickname, avatar_version FROM users ORDER BY username")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	profiles := []Profile{}
	for rows.Next() {
		var profile Profile
		if err := rows.Scan(&profile.ID, &profile.Username, &profile.Nickname, &profile.AvatarVersion); err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

// SetNickname changes the name the actor is shown by; an empty nickname
// shows the username again.
func (s *Service) SetNickname(ctx context.Context, actor User, nickname string) (User, error) {
	if actor.Status != UserStatusActive {
		return User{}, ErrForbidden
	}
	nickname, err := normalizeNickname(nickname)
	if err != nil {
		return User{}, err
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var previous string
	if err := tx.QueryRowContext(ctx, "SELECT nickname FROM users WHERE id = ?", actor.ID).Scan(&previous); errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUserNotFound
	} else if err != nil {
		return User{}, err
	}
	if nickname != previous {
		if nickname != "" {
			// The store keeps one connection, so this check and the update
			// below cannot interleave with another account's.
			if taken, err := nameTaken(ctx, tx, actor.ID, nickname); err != nil {
				return User{}, err
			} else if taken {
				return User{}, ErrNicknameUnavailable
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE users SET nickname = ? WHERE id = ?", nickname, actor.ID); err != nil {
			return User{}, err
		}
		detail, _ := json.Marshal(struct {
			From string `json:"from"`
			To   string `json:"to"`
		}{previous, nickname})
		if err := s.appendAuditTo(ctx, tx, actor.ID, "user.nickname_changed", "user", actor.ID, string(detail)); err != nil {
			return User{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return s.user(ctx, actor.ID)
}

// SetAvatar stores image, which the caller has already decoded, cropped and
// compressed, as the actor's avatar. Its version is a digest of the image,
// so a changed avatar gets a new URL and an unchanged one keeps its cache.
func (s *Service) SetAvatar(ctx context.Context, actor User, image []byte, contentType string) (User, error) {
	if actor.Status != UserStatusActive {
		return User{}, ErrForbidden
	}
	if len(image) == 0 || !strings.HasPrefix(contentType, "image/") {
		return User{}, errors.New("avatar image is empty or not an image")
	}
	digest := sha256.Sum256(image)
	version := hex.EncodeToString(digest[:8])
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO avatars(user_id, version, content_type, image, updated_at) VALUES(?,?,?,?,?)
ON CONFLICT(user_id) DO UPDATE SET version = excluded.version, content_type = excluded.content_type, image = excluded.image, updated_at = excluded.updated_at`,
		actor.ID, version, contentType, image, formatTime(s.now())); err != nil {
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE users SET avatar_version = ? WHERE id = ?", version, actor.ID); err != nil {
		return User{}, err
	}
	if err := s.appendAuditTo(ctx, tx, actor.ID, "user.avatar_changed", "user", actor.ID, ""); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return s.user(ctx, actor.ID)
}

// RemoveAvatar returns the actor to the initial on a coloured disc.
func (s *Service) RemoveAvatar(ctx context.Context, actor User) (User, error) {
	if actor.Status != UserStatusActive {
		return User{}, ErrForbidden
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, "DELETE FROM avatars WHERE user_id = ?", actor.ID)
	if err != nil {
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE users SET avatar_version = '' WHERE id = ?", actor.ID); err != nil {
		return User{}, err
	}
	if removed, _ := result.RowsAffected(); removed > 0 {
		if err := s.appendAuditTo(ctx, tx, actor.ID, "user.avatar_removed", "user", actor.ID, ""); err != nil {
			return User{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return s.user(ctx, actor.ID)
}

// Avatar reads the avatar of userID for any signed-in user.
func (s *Service) Avatar(ctx context.Context, userID string) (Avatar, error) {
	var avatar Avatar
	var updatedAt string
	err := s.store.db.QueryRowContext(ctx, "SELECT version, content_type, image, updated_at FROM avatars WHERE user_id = ?", userID).Scan(
		&avatar.Version, &avatar.ContentType, &avatar.Image, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Avatar{}, ErrAvatarNotFound
	}
	if err != nil {
		return Avatar{}, err
	}
	avatar.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return avatar, nil
}

func (s *Service) user(ctx context.Context, userID string) (User, error) {
	user, err := scanUser(s.store.db.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id = ?", userID))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	return user, err
}

// normalizeNickname trims a nickname, folds runs of spaces into one and
// checks the rules; it returns "" for a blank one.
func normalizeNickname(nickname string) (string, error) {
	if !utf8.ValidString(nickname) {
		return "", ErrNicknameCharacters
	}
	nickname = norm.NFC.String(nickname)
	for _, r := range nickname {
		if r != zeroWidthJoiner && !unicode.IsGraphic(r) {
			return "", ErrNicknameCharacters
		}
	}
	nickname = strings.Join(strings.Fields(nickname), " ")
	if utf8.RuneCountInString(nickname) > MaxNicknameLength {
		return "", ErrNicknameTooLong
	}
	return nickname, nil
}

// nameTaken reports whether an account other than exceptUserID is shown as
// name: as its username, or as its nickname. Names compare ignoring case.
func nameTaken(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, exceptUserID, name string) (bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT username, nickname FROM users WHERE id <> ?", exceptUserID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var username, nickname string
		if err := rows.Scan(&username, &nickname); err != nil {
			return false, err
		}
		if strings.EqualFold(username, name) || (nickname != "" && strings.EqualFold(nickname, name)) {
			return true, nil
		}
	}
	return false, rows.Err()
}
