package accounts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/accountid"
)

// Device tokens are the family-level credentials of the e-ink screen (M1,
// ADR 0041). Unlike agent tokens they are not bound to an account: the screen
// belongs to the whole family, so only an administrator creates, lists and
// revokes them, and they survive any one account's disable, reset or
// deletion. The token value is returned exactly once, by CreateDeviceToken.
const (
	// MaxDeviceTokens is how many screens one family may register.
	MaxDeviceTokens = 10
	// MaxDeviceTokenName bounds the name an administrator gives a screen.
	MaxDeviceTokenName = 40
	// DefaultDevicePollSeconds is the daytime poll interval until an
	// administrator chooses otherwise.
	DefaultDevicePollSeconds = 60
	// MinDevicePollSeconds and MaxDevicePollSeconds bound the interval.
	MinDevicePollSeconds = 15
	MaxDevicePollSeconds = 3600
	// deviceTokenTouch is how stale a token's last use may get before a use
	// is written: the screen polls at most every 15 seconds.
	deviceTokenTouch = 5 * time.Minute
)

var (
	// ErrDeviceTokenLimit reports a family that already holds MaxDeviceTokens.
	ErrDeviceTokenLimit = errors.New("a family holds at most 10 device tokens; revoke one first")
	// ErrDeviceTokenNotFound reports a token that is not known or was revoked.
	ErrDeviceTokenNotFound = errors.New("device token not found")
	// ErrDeviceTokenInvalid is what a screen learns about a token it cannot
	// use, whatever the reason (accountid.ErrDeviceTokenNotFound).
	ErrDeviceTokenInvalid = accountid.ErrDeviceTokenNotFound
)

// DeviceToken is one e-ink screen the family registered (ADR 0041). The token
// itself is only ever returned by CreateDeviceToken.
type DeviceToken struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	PollSeconds int        `json:"pollSeconds"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastUsedAt  *time.Time `json:"lastUsedAt,omitempty"`
}

// CreateDeviceToken issues a token for one screen and returns it with the
// token's value, which is not kept.
func (s *Service) CreateDeviceToken(ctx context.Context, actor User, name string, pollSeconds int) (DeviceToken, string, error) {
	if !actor.ActiveAdministrator() {
		return DeviceToken{}, "", ErrForbidden
	}
	name = strings.TrimSpace(name)
	if !validDeviceTokenName(name) || !validDevicePollSeconds(pollSeconds) {
		return DeviceToken{}, "", fmt.Errorf("%w: name the screen in 1–%d characters and poll every %d–%d seconds",
			ErrInvalidInput, MaxDeviceTokenName, MinDevicePollSeconds, MaxDevicePollSeconds)
	}
	now := s.now().UTC()
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return DeviceToken{}, "", err
	}
	defer func() { _ = tx.Rollback() }()
	if err := deleteIdleDeviceTokens(ctx, tx, now); err != nil {
		return DeviceToken{}, "", err
	}
	var held int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM device_tokens").Scan(&held); err != nil {
		return DeviceToken{}, "", err
	}
	if held >= MaxDeviceTokens {
		return DeviceToken{}, "", ErrDeviceTokenLimit
	}
	value := accountid.DeviceTokenPrefix + s.randomToken(32)
	token := DeviceToken{ID: s.randomID("device"), Name: name, PollSeconds: pollSeconds, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO device_tokens(id, token_hash, name, poll_seconds, created_at) VALUES(?,?,?,?,?)`,
		token.ID, tokenHash(value), token.Name, token.PollSeconds, formatTime(now)); err != nil {
		return DeviceToken{}, "", err
	}
	if err := s.appendAuditTo(ctx, tx, actor.ID, "device_token_created", "device_token", token.ID, token.Name); err != nil {
		return DeviceToken{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return DeviceToken{}, "", err
	}
	return token, value, nil
}

// DeviceTokens lists every screen, newest first; tokens idle past
// accountid.DeviceTokenIdleLimit are gone.
func (s *Service) DeviceTokens(ctx context.Context, actor User) ([]DeviceToken, error) {
	if !actor.ActiveAdministrator() {
		return nil, ErrForbidden
	}
	if err := deleteIdleDeviceTokens(ctx, s.store.db, s.now().UTC()); err != nil {
		return nil, err
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT id, name, poll_seconds, created_at, last_used_at FROM device_tokens
ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tokens := []DeviceToken{}
	for rows.Next() {
		token, err := scanDeviceToken(rows.Scan)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

// RevokeDeviceToken ends one screen's registration.
func (s *Service) RevokeDeviceToken(ctx context.Context, actor User, tokenID string) error {
	if !actor.ActiveAdministrator() {
		return ErrForbidden
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var name string
	err = tx.QueryRowContext(ctx, "DELETE FROM device_tokens WHERE id = ? RETURNING name", tokenID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDeviceTokenNotFound
	}
	if err != nil {
		return err
	}
	if err := s.appendAuditTo(ctx, tx, actor.ID, "device_token_revoked", "device_token", tokenID, name); err != nil {
		return err
	}
	return tx.Commit()
}

// deviceTokenAttempts is the name device token attempts count under in the
// sign-in failures: a client that keeps presenting bad tokens waits as one
// that keeps guessing a password does (#175).
const deviceTokenAttempts = "\x00device token"

// ResolveDeviceToken returns the screen that calls with value. It fails with
// ErrDeviceTokenInvalid for a token that is unknown or idle past the limit:
// the Product Service's guard refuses both alike. The client
// (WithClientOrigin) waits after repeated failures, with a *ThrottledError.
// A use is recorded at most every deviceTokenTouch.
func (s *Service) ResolveDeviceToken(ctx context.Context, value string) (DeviceToken, error) {
	client := originFrom(ctx).throttleClient()
	if wait := s.signIns.wait(s.now(), client, deviceTokenAttempts); wait > 0 {
		return DeviceToken{}, &ThrottledError{RetryAfter: wait}
	}
	token, err := s.resolveDeviceToken(ctx, value)
	if errors.Is(err, ErrDeviceTokenInvalid) {
		s.signIns.failed(s.now(), client, deviceTokenAttempts)
	} else if err == nil {
		s.signIns.succeeded(client, deviceTokenAttempts)
	}
	return token, err
}

func (s *Service) resolveDeviceToken(ctx context.Context, value string) (DeviceToken, error) {
	if !accountid.LooksLikeDeviceToken(value) {
		return DeviceToken{}, ErrDeviceTokenInvalid
	}
	token, err := scanDeviceToken(s.store.db.QueryRowContext(ctx, `
SELECT id, name, poll_seconds, created_at, last_used_at
FROM device_tokens WHERE token_hash = ?`, tokenHash(value)).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return DeviceToken{}, ErrDeviceTokenInvalid
	}
	if err != nil {
		return DeviceToken{}, err
	}
	lastUse := token.CreatedAt
	if token.LastUsedAt != nil {
		lastUse = *token.LastUsedAt
	}
	now := s.now().UTC()
	if accountid.DeviceTokenIdle(lastUse, now) {
		_, _ = s.store.db.ExecContext(ctx, "DELETE FROM device_tokens WHERE id = ?", token.ID)
		return DeviceToken{}, ErrDeviceTokenInvalid
	}
	if token.LastUsedAt == nil || now.Sub(*token.LastUsedAt) >= deviceTokenTouch {
		if _, err := s.store.db.ExecContext(ctx, "UPDATE device_tokens SET last_used_at = ? WHERE id = ?", formatTime(now), token.ID); err != nil {
			return DeviceToken{}, err
		}
		token.LastUsedAt = &now
	}
	return token, nil
}

// deleteIdleDeviceTokens forgets every token nobody used for
// accountid.DeviceTokenIdleLimit.
func deleteIdleDeviceTokens(ctx context.Context, db sqlExecutor, now time.Time) error {
	_, err := db.ExecContext(ctx, "DELETE FROM device_tokens WHERE COALESCE(last_used_at, created_at) <= ?",
		formatTime(now.Add(-accountid.DeviceTokenIdleLimit)))
	return err
}

// scanDeviceToken reads a token's columns, then extra.
func scanDeviceToken(scan func(...any) error, extra ...any) (DeviceToken, error) {
	var token DeviceToken
	var createdAt string
	var lastUsed sql.NullString
	if err := scan(append([]any{&token.ID, &token.Name, &token.PollSeconds, &createdAt, &lastUsed}, extra...)...); err != nil {
		return DeviceToken{}, err
	}
	token.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	if lastUsed.Valid {
		used, _ := time.Parse(time.RFC3339Nano, lastUsed.String)
		token.LastUsedAt = &used
	}
	return token, nil
}

func validDeviceTokenName(name string) bool {
	if name == "" || utf8.RuneCountInString(name) > MaxDeviceTokenName {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validDevicePollSeconds(seconds int) bool {
	return seconds >= MinDevicePollSeconds && seconds <= MaxDevicePollSeconds
}
