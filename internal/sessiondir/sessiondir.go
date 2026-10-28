// Package sessiondir reads the Product Service's session and account tables
// read-only for root: the file broker, terminals, assistants and the photo
// and share lookups verify callers through it, never through the Product
// Service's word (ADR 0008).
package sessiondir

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// Directory resolves session tokens to Linux identities for the File Broker.
// The broker runs as root and must not trust the Product Service's claim
// about who is asking, so it reads the session table itself.
type Directory struct {
	path string
	now  func() time.Time
	mu   sync.Mutex
	db   *sql.DB
}

func New(path string) *Directory {
	return &Directory{path: path, now: time.Now}
}

// ResolveSessionIdentity returns the identity of an active, unexpired session.
// A session whose user must first change the password an administrator chose
// acts on no files: the Product Service refuses it too, but the broker does
// not rely on that.
func (d *Directory) ResolveSessionIdentity(ctx context.Context, token string) (accountid.Identity, error) {
	if token == "" {
		return accountid.Identity{}, accountid.ErrSessionNotFound
	}
	db, err := d.open()
	if err != nil {
		return accountid.Identity{}, err
	}
	var account accountid.Identity
	var status accountid.UserStatus
	var mustChangePassword bool
	var expiresAt string
	err = db.QueryRowContext(ctx, `
SELECT u.username, u.role, u.status, u.must_change_password, s.expires_at, i.uid
FROM sessions s
JOIN users u ON u.id = s.user_id
JOIN linux_identities i ON i.user_id = u.id
WHERE s.token_hash = ?`, accountid.TokenHash(token)).Scan(&account.Username, &account.Role, &status, &mustChangePassword, &expiresAt, &account.UID)
	if errors.Is(err, sql.ErrNoRows) {
		return accountid.Identity{}, accountid.ErrSessionNotFound
	}
	if err != nil {
		return accountid.Identity{}, fmt.Errorf("read session directory: %w", err)
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || status != accountid.UserStatusActive || mustChangePassword || !d.now().UTC().Before(expires) {
		return accountid.Identity{}, accountid.ErrSessionNotFound
	}
	account.Enabled = true
	return account, nil
}

// ResolveSessionUser returns the user of an active, unexpired session. The
// photo service learns who is asking through it (ADR 0011).
func (d *Directory) ResolveSessionUser(ctx context.Context, token string) (accountid.SessionUser, error) {
	if token == "" {
		return accountid.SessionUser{}, accountid.ErrSessionNotFound
	}
	db, err := d.open()
	if err != nil {
		return accountid.SessionUser{}, err
	}
	var user accountid.SessionUser
	var status accountid.UserStatus
	var expiresAt string
	err = db.QueryRowContext(ctx, `
SELECT u.id, u.username, u.role, u.status, u.must_change_password, s.expires_at
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.token_hash = ?`, accountid.TokenHash(token)).Scan(&user.UserID, &user.Username, &user.Role, &status, &user.MustChangePassword, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return accountid.SessionUser{}, accountid.ErrSessionNotFound
	}
	if err != nil {
		return accountid.SessionUser{}, fmt.Errorf("read session directory: %w", err)
	}
	now := d.now().UTC()
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || status != accountid.UserStatusActive || !now.Before(expires) {
		return accountid.SessionUser{}, accountid.ErrSessionNotFound
	}
	if user.Role == accountid.RoleAdmin {
		if user.Viewing, err = LibraryViewings(ctx, db, user.UserID, now); err != nil {
			return accountid.SessionUser{}, fmt.Errorf("read viewing grants: %w", err)
		}
	}
	return user, nil
}

// ResolveAccountUID returns the active user who owns the Linux UID, without
// any viewing grants. A member's AI assistant runs as the member's own UID
// (ADR 0024), so the photo service learns whom it acts for from the kernel's
// peer credentials and this lookup, never from the Product Service.
func (d *Directory) ResolveAccountUID(ctx context.Context, uid int) (accountid.SessionUser, error) {
	db, err := d.open()
	if err != nil {
		return accountid.SessionUser{}, err
	}
	var user accountid.SessionUser
	var status accountid.UserStatus
	err = db.QueryRowContext(ctx, `
SELECT u.id, u.username, u.role, u.status, u.must_change_password
FROM linux_identities l
JOIN users u ON u.id = l.user_id
WHERE l.uid = ?`, uid).Scan(&user.UserID, &user.Username, &user.Role, &status, &user.MustChangePassword)
	if errors.Is(err, sql.ErrNoRows) {
		return accountid.SessionUser{}, accountid.ErrUserNotFound
	}
	if err != nil {
		return accountid.SessionUser{}, fmt.Errorf("read account directory: %w", err)
	}
	if status != accountid.UserStatusActive {
		return accountid.SessionUser{}, accountid.ErrUserNotFound
	}
	return user, nil
}

// open connects read-only, so the root broker never writes the database or
// creates SQLite side files owned by root next to the Product Service's.
//
// A failed query leaves the handle open: the File Broker, terminals,
// assistants and the photo and share lookups all share it, and closing it
// after one error failed every lookup waiting on it (#166). The connection is
// reopened every minute instead, so one that went bad, or a database file
// that was replaced, is picked up without interrupting anyone.
func (d *Directory) open() (*sql.DB, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.db != nil {
		return d.db, nil
	}
	db, err := sqlitedb.OpenReadOnly(d.path)
	if err != nil {
		return nil, err
	}
	db.SetConnMaxLifetime(time.Minute)
	d.db = db
	return db, nil
}

func (d *Directory) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.db != nil {
		_ = d.db.Close()
		d.db = nil
	}
	return nil
}

// ActiveProfiles is the sharing service's recipient directory. The private
// lookup socket exposes display identities only, never credentials or sessions.
func (d *Directory) ActiveProfiles(ctx context.Context) ([]accountid.Profile, error) {
	db, err := d.open()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "SELECT id, username, nickname, avatar_version FROM users WHERE status = ? ORDER BY username", accountid.UserStatusActive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	profiles := []accountid.Profile{}
	for rows.Next() {
		var profile accountid.Profile
		if err := rows.Scan(&profile.ID, &profile.Username, &profile.Nickname, &profile.AvatarVersion); err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

// LibraryViewings returns an administrator's unexpired grants to view
// members' private photo libraries.
func LibraryViewings(ctx context.Context, db *sql.DB, adminUserID string, now time.Time) ([]accountid.LibraryViewing, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, owner_user_id, expires_at FROM viewing_grants
WHERE admin_user_id = ? AND scope = ? AND ended_at IS NULL AND expires_at > ? ORDER BY expires_at`,
		adminUserID, accountid.ViewingScopeLibrary, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var viewings []accountid.LibraryViewing
	for rows.Next() {
		var viewing accountid.LibraryViewing
		var expiresAt string
		if err := rows.Scan(&viewing.GrantID, &viewing.OwnerUserID, &expiresAt); err != nil {
			return nil, err
		}
		if viewing.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresAt); err != nil {
			return nil, err
		}
		viewings = append(viewings, viewing)
	}
	return viewings, rows.Err()
}
