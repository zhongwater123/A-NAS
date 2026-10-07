// Package accounts owns A-NAS users, credentials, sessions, and space membership.
package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/argon2"
)

var (
	ErrSetupComplete       = errors.New("administrator setup is already complete")
	ErrInvalidUsername     = errors.New("invalid username")
	ErrWeakPassword        = errors.New("password must contain at least 12 characters")
	ErrInvalidCredentials  = errors.New("invalid username or password")
	ErrSessionNotFound     = errors.New("session is invalid or expired")
	ErrUsernameUnavailable = errors.New("username is unavailable")
	ErrCredentialProvision = errors.New("account credential provisioning failed")
	ErrForbidden           = errors.New("operation is forbidden")
	ErrUserNotFound        = errors.New("user not found")
	// ErrIdentityConflict reports that the host already has an account,
	// group, or UID that A-NAS did not allocate for this user.
	ErrIdentityConflict       = errors.New("username or Linux identity conflicts with an existing host account")
	ErrIdentityRangeExhausted = errors.New("A-NAS Linux identity range is exhausted")
)

// Linux identity layout shared with the Host Agent (ADR 0008). Fixed group
// GIDs and user UIDs are recorded on the data volume, so they must never change.
const (
	UsersGroup   = "a-nas-users"
	AdminsGroup  = "a-nas-admins"
	UsersGID     = 20000
	AdminsGID    = 20001
	FirstUserUID = 20100
	LastUserUID  = 29999
)

type Role string

const (
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

type UserStatus string

const (
	UserStatusPending  UserStatus = "pending"
	UserStatusActive   UserStatus = "active"
	UserStatusDisabled UserStatus = "disabled"
	UserStatusError    UserStatus = "error"
)

type User struct {
	ID        string     `json:"id"`
	Username  string     `json:"username"`
	Role      Role       `json:"role"`
	Status    UserStatus `json:"status"`
	CreatedAt time.Time  `json:"createdAt"`
}

type SpaceKind string

const (
	SpaceKindPrivate SpaceKind = "private"
	SpaceKindShared  SpaceKind = "shared"
)

type Space struct {
	ID          string    `json:"id"`
	Kind        SpaceKind `json:"kind"`
	Name        string    `json:"name"`
	OwnerUserID string    `json:"ownerUserId,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

type Session struct {
	Token     string    `json:"token,omitempty"`
	CSRFToken string    `json:"csrfToken"`
	ExpiresAt time.Time `json:"expiresAt"`
	User      User      `json:"user"`
}

type AuditEvent struct {
	ID           string    `json:"id"`
	ActorUserID  string    `json:"actorUserId,omitempty"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resourceType"`
	ResourceID   string    `json:"resourceId"`
	OccurredAt   time.Time `json:"occurredAt"`
	Detail       string    `json:"detail,omitempty"`
}

type CredentialRequest struct {
	UserID         string
	PrivateSpaceID string
	Username       string
	Password       string
	Role           Role
	UID            int
	// Enabled is false when an administrator resets the credential of a
	// disabled account; the SMB credential must stay disabled.
	Enabled bool
}

type CredentialProvisioner interface {
	SetCredential(context.Context, CredentialRequest) error
	DisableCredential(context.Context, string) error
}

// Identity is the Linux account the Host Agent keeps for one A-NAS user.
type Identity struct {
	Username string
	UID      int
	Role     Role
	Enabled  bool
}

// IdentitySynchronizer converges host accounts and groups on the control plane
// without changing any password.
type IdentitySynchronizer interface {
	SyncIdentities(context.Context, []Identity) error
}

type Options struct {
	Now    func() time.Time
	Random io.Reader
}

type Store struct {
	db *sql.DB
}

func OpenSQLite(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("SQLite path is empty")
	}
	dsn := path + "?_foreign_keys=on&_busy_timeout=5000&_journal_mode=WAL"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL UNIQUE COLLATE NOCASE,
    role TEXT NOT NULL CHECK (role IN ('admin', 'member')),
    status TEXT NOT NULL CHECK (status IN ('pending', 'active', 'disabled', 'error')),
    password_hash TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
    token_hash TEXT PRIMARY KEY,
    csrf_token TEXT NOT NULL,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS spaces (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('private', 'shared')),
    name TEXT NOT NULL,
    owner_user_id TEXT REFERENCES users(id),
    created_at TEXT NOT NULL,
    UNIQUE(kind, owner_user_id)
);
CREATE TABLE IF NOT EXISTS audit_events (
    id TEXT PRIMARY KEY,
    actor_user_id TEXT,
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    occurred_at TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT ''
);
-- UIDs are never deleted or reused: files on the data volume keep the UID
-- after the account is gone. No foreign key, so the row outlives the user.
CREATE TABLE IF NOT EXISTS linux_identities (
    uid INTEGER PRIMARY KEY CHECK (uid BETWEEN 20100 AND 29999),
    user_id TEXT NOT NULL UNIQUE,
    allocated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_user_id ON sessions(user_id);
CREATE INDEX IF NOT EXISTS audit_occurred_at ON audit_events(occurred_at);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate SQLite: %w", err)
	}
	return s.allocateMissingIdentities(ctx)
}

// allocateMissingIdentities gives accounts created before ADR 0008 a UID in
// creation order. Their host accounts are converged by SyncIdentities.
func (s *Store) allocateMissingIdentities(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT u.id FROM users u
LEFT JOIN linux_identities i ON i.user_id = u.id
WHERE i.uid IS NULL ORDER BY u.created_at, u.id`)
	if err != nil {
		return err
	}
	var userIDs []string
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			_ = rows.Close()
			return err
		}
		userIDs = append(userIDs, userID)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, userID := range userIDs {
		if _, err := allocateUID(ctx, s.db, userID, time.Now().UTC()); err != nil {
			return fmt.Errorf("allocate Linux identity: %w", err)
		}
	}
	return nil
}

type sqlExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func allocateUID(ctx context.Context, db sqlExecutor, userID string, now time.Time) (int, error) {
	var last int
	if err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(uid), ?) FROM linux_identities", FirstUserUID-1).Scan(&last); err != nil {
		return 0, err
	}
	uid := last + 1
	if uid > LastUserUID {
		return 0, ErrIdentityRangeExhausted
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO linux_identities(uid, user_id, allocated_at) VALUES(?,?,?)",
		uid, userID, formatTime(now)); err != nil {
		return 0, err
	}
	return uid, nil
}

func (s *Service) uidForUser(ctx context.Context, userID string) (int, error) {
	var uid int
	if err := s.store.db.QueryRowContext(ctx, "SELECT uid FROM linux_identities WHERE user_id = ?", userID).Scan(&uid); err != nil {
		return 0, fmt.Errorf("read Linux identity: %w", err)
	}
	return uid, nil
}

type Service struct {
	store       *Store
	credentials CredentialProvisioner
	now         func() time.Time
	random      io.Reader
	sessions    sessionMemory
}

func NewService(store *Store, credentials CredentialProvisioner, options Options) *Service {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	random := options.Random
	if random == nil {
		random = rand.Reader
	}
	return &Service{store: store, credentials: credentials, now: now, random: random}
}

func (s *Service) SetupAdministrator(ctx context.Context, username, password string) (User, error) {
	var count int
	if err := s.store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return User{}, err
	}
	if count == 0 {
		return s.createUser(ctx, username, password, RoleAdmin, "")
	}
	if count != 1 {
		return User{}, ErrSetupComplete
	}
	username = strings.ToLower(strings.TrimSpace(username))
	var user User
	var createdAt, privateSpaceID string
	err := s.store.db.QueryRowContext(ctx, `SELECT u.id, u.username, u.role, u.status, u.created_at, s.id
FROM users u JOIN spaces s ON s.owner_user_id = u.id AND s.kind = 'private'`).Scan(
		&user.ID, &user.Username, &user.Role, &user.Status, &createdAt, &privateSpaceID)
	if err != nil || user.Role != RoleAdmin || user.Username != username || (user.Status != UserStatusPending && user.Status != UserStatusError) {
		return User{}, ErrSetupComplete
	}
	if len([]rune(password)) < 12 {
		return User{}, ErrWeakPassword
	}
	passwordHash, err := hashPassword(password, s.random)
	if err != nil {
		return User{}, err
	}
	if s.credentials == nil {
		return s.markCredentialFailure(ctx, user, errors.New("credential provisioner is unavailable"))
	}
	uid, err := s.uidForUser(ctx, user.ID)
	if err != nil {
		return User{}, err
	}
	if err := s.credentials.SetCredential(ctx, CredentialRequest{
		UserID: user.ID, PrivateSpaceID: privateSpaceID, Username: user.Username, Password: password, Role: user.Role,
		UID: uid, Enabled: true,
	}); err != nil {
		return s.handleProvisionFailure(ctx, "", user, err)
	}
	if _, err := s.store.db.ExecContext(ctx, "UPDATE users SET password_hash = ?, status = 'active' WHERE id = ?", passwordHash, user.ID); err != nil {
		return User{}, err
	}
	user.Status = UserStatusActive
	user.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	_ = s.appendAudit(ctx, "", "user.setup_recovered", "user", user.ID, "admin")
	return user, nil
}

func (s *Service) SetupRequired(ctx context.Context) (bool, error) {
	var count int
	if err := s.store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE role = 'admin' AND status = 'active'").Scan(&count); err != nil {
		return false, err
	}
	return count == 0, nil
}

func (s *Service) CreateMember(ctx context.Context, actor User, username, password string) (User, error) {
	if actor.Role != RoleAdmin || actor.Status != UserStatusActive {
		return User{}, ErrForbidden
	}
	return s.createUser(ctx, username, password, RoleMember, actor.ID)
}

func (s *Service) createUser(ctx context.Context, username, password string, role Role, actorID string) (User, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if !validUsername(username) {
		return User{}, ErrInvalidUsername
	}
	if len([]rune(password)) < 12 {
		return User{}, ErrWeakPassword
	}
	passwordHash, err := hashPassword(password, s.random)
	if err != nil {
		return User{}, err
	}
	now := s.now().UTC()
	user := User{ID: s.randomID("user"), Username: username, Role: role, Status: UserStatusPending, CreatedAt: now}
	privateSpaceID := s.randomID("space")
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO users(id, username, role, status, password_hash, created_at) VALUES(?,?,?,?,?,?)",
		user.ID, user.Username, user.Role, user.Status, passwordHash, formatTime(now)); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return User{}, ErrUsernameUnavailable
		}
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO spaces(id, kind, name, owner_user_id, created_at) VALUES(?,?,?,?,?)",
		privateSpaceID, "private", user.Username, user.ID, formatTime(now)); err != nil {
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT OR IGNORE INTO spaces(id, kind, name, owner_user_id, created_at) VALUES('space:shared','shared','Shared',NULL,?)",
		formatTime(now)); err != nil {
		return User{}, err
	}
	uid, err := allocateUID(ctx, tx, user.ID, now)
	if err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}

	if s.credentials == nil {
		return s.markCredentialFailure(ctx, user, errors.New("credential provisioner is unavailable"))
	}
	if err := s.credentials.SetCredential(ctx, CredentialRequest{
		UserID: user.ID, PrivateSpaceID: privateSpaceID, Username: user.Username, Password: password, Role: user.Role,
		UID: uid, Enabled: true,
	}); err != nil {
		return s.handleProvisionFailure(ctx, actorID, user, err)
	}
	if _, err := s.store.db.ExecContext(ctx, "UPDATE users SET status = 'active' WHERE id = ? AND status = 'pending'", user.ID); err != nil {
		return User{}, err
	}
	user.Status = UserStatusActive
	_ = s.appendAudit(ctx, actorID, "user.created", "user", user.ID, string(role))
	return user, nil
}

// handleProvisionFailure discards a never-provisioned account whose name or
// UID collides with a host account, so the name can be chosen again. Its UID
// stays allocated and is never reused.
func (s *Service) handleProvisionFailure(ctx context.Context, actorID string, user User, cause error) (User, error) {
	if !errors.Is(cause, ErrIdentityConflict) {
		return s.markCredentialFailure(ctx, user, cause)
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "DELETE FROM spaces WHERE owner_user_id = ?", user.ID); err != nil {
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM users WHERE id = ? AND status IN ('pending', 'error')", user.ID); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	_ = s.appendAudit(ctx, actorID, "user.identity_conflict", "user", user.ID, user.Username)
	return User{}, ErrIdentityConflict
}

func (s *Service) markCredentialFailure(ctx context.Context, user User, cause error) (User, error) {
	_, _ = s.store.db.ExecContext(ctx, "UPDATE users SET status = 'error' WHERE id = ?", user.ID)
	user.Status = UserStatusError
	return user, fmt.Errorf("%w: %v", ErrCredentialProvision, cause)
}

func (s *Service) Authenticate(ctx context.Context, username, password string) (Session, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	var user User
	var encodedHash, createdAt string
	err := s.store.db.QueryRowContext(ctx,
		"SELECT id, username, role, status, password_hash, created_at FROM users WHERE username = ?", username,
	).Scan(&user.ID, &user.Username, &user.Role, &user.Status, &encodedHash, &createdAt)
	if err != nil || user.Status != UserStatusActive || !verifyPassword(password, encodedHash) {
		return Session{}, ErrInvalidCredentials
	}
	user.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	token := s.randomToken(32)
	csrf := s.randomToken(24)
	now := s.now().UTC()
	expiresAt := now.Add(12 * time.Hour)
	if _, err := s.store.db.ExecContext(ctx,
		"INSERT INTO sessions(token_hash, csrf_token, user_id, expires_at, created_at) VALUES(?,?,?,?,?)",
		tokenHash(token), csrf, user.ID, formatTime(expiresAt), formatTime(now)); err != nil {
		return Session{}, err
	}
	s.sessions.remember(user.ID, token, expiresAt)
	return Session{Token: token, CSRFToken: csrf, ExpiresAt: expiresAt, User: user}, nil
}

func (s *Service) ResolveSession(ctx context.Context, token string) (Session, error) {
	var session Session
	var expiresAt, createdAt string
	err := s.store.db.QueryRowContext(ctx, `
SELECT s.csrf_token, s.expires_at, u.id, u.username, u.role, u.status, u.created_at
FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.token_hash = ?`, tokenHash(token)).Scan(
		&session.CSRFToken, &expiresAt, &session.User.ID, &session.User.Username,
		&session.User.Role, &session.User.Status, &createdAt,
	)
	if err != nil {
		return Session{}, ErrSessionNotFound
	}
	session.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expiresAt)
	session.User.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	if session.User.Status != UserStatusActive || !s.now().UTC().Before(session.ExpiresAt) {
		_, _ = s.store.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", tokenHash(token))
		s.sessions.forgetToken(token)
		return Session{}, ErrSessionNotFound
	}
	s.sessions.remember(session.User.ID, token, session.ExpiresAt)
	return session, nil
}

func (s *Service) EndSession(ctx context.Context, token string) error {
	s.sessions.forgetToken(token)
	_, err := s.store.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", tokenHash(token))
	return err
}

func (s *Service) ListUsers(ctx context.Context, actor User) ([]User, error) {
	if actor.Role != RoleAdmin || actor.Status != UserStatusActive {
		return nil, ErrForbidden
	}
	rows, err := s.store.db.QueryContext(ctx, "SELECT id, username, role, status, created_at FROM users ORDER BY username")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var user User
		var createdAt string
		if err := rows.Scan(&user.ID, &user.Username, &user.Role, &user.Status, &createdAt); err != nil {
			return nil, err
		}
		user.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		users = append(users, user)
	}
	return users, rows.Err()
}

func (s *Service) ActiveUsers(ctx context.Context) ([]User, error) {
	rows, err := s.store.db.QueryContext(ctx, "SELECT id, username, role, status, created_at FROM users WHERE status = 'active' ORDER BY username")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		var user User
		var createdAt string
		if err := rows.Scan(&user.ID, &user.Username, &user.Role, &user.Status, &createdAt); err != nil {
			return nil, err
		}
		user.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		users = append(users, user)
	}
	return users, rows.Err()
}

func (s *Service) ListAudit(ctx context.Context, actor User) ([]AuditEvent, error) {
	if actor.Role != RoleAdmin || actor.Status != UserStatusActive {
		return nil, ErrForbidden
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT id, actor_user_id, action, resource_type, resource_id, occurred_at, detail
FROM audit_events ORDER BY occurred_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []AuditEvent
	for rows.Next() {
		var event AuditEvent
		var actorID sql.NullString
		var occurredAt string
		if err := rows.Scan(&event.ID, &actorID, &event.Action, &event.ResourceType, &event.ResourceID, &occurredAt, &event.Detail); err != nil {
			return nil, err
		}
		event.ActorUserID = actorID.String
		event.OccurredAt, _ = time.Parse(time.RFC3339Nano, occurredAt)
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Service) ListSpaces(ctx context.Context, actor User) ([]Space, error) {
	if actor.Status != UserStatusActive {
		return nil, ErrForbidden
	}
	rows, err := s.store.db.QueryContext(ctx, `
SELECT id, kind, name, COALESCE(owner_user_id, ''), created_at
FROM spaces
WHERE kind = 'shared' OR (kind = 'private' AND owner_user_id = ?)
ORDER BY kind, name`, actor.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var spaces []Space
	for rows.Next() {
		var space Space
		var createdAt string
		if err := rows.Scan(&space.ID, &space.Kind, &space.Name, &space.OwnerUserID, &createdAt); err != nil {
			return nil, err
		}
		space.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		spaces = append(spaces, space)
	}
	return spaces, rows.Err()
}

func (s *Service) CanAccessSpace(ctx context.Context, actor User, spaceID string, _ bool) (bool, error) {
	if actor.Status != UserStatusActive {
		return false, nil
	}
	var kind SpaceKind
	var ownerID string
	err := s.store.db.QueryRowContext(ctx,
		"SELECT kind, COALESCE(owner_user_id, '') FROM spaces WHERE id = ?", spaceID,
	).Scan(&kind, &ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return kind == SpaceKindShared || (kind == SpaceKindPrivate && ownerID == actor.ID), nil
}

func (s *Service) UserIDForUsername(ctx context.Context, username string) (string, error) {
	var userID string
	if err := s.store.db.QueryRowContext(ctx, "SELECT id FROM users WHERE username = ?", strings.ToLower(strings.TrimSpace(username))).Scan(&userID); errors.Is(err, sql.ErrNoRows) {
		return "", ErrUserNotFound
	} else if err != nil {
		return "", err
	}
	return userID, nil
}

func (s *Service) DisableUser(ctx context.Context, actor User, userID string) error {
	if actor.Role != RoleAdmin || actor.Status != UserStatusActive || actor.ID == userID {
		return ErrForbidden
	}
	var username string
	if err := s.store.db.QueryRowContext(ctx, "SELECT username FROM users WHERE id = ?", userID).Scan(&username); errors.Is(err, sql.ErrNoRows) {
		return ErrUserNotFound
	} else if err != nil {
		return err
	}
	if s.credentials == nil {
		return errors.New("credential provisioner is unavailable")
	}
	if err := s.credentials.DisableCredential(ctx, username); err != nil {
		return err
	}
	if _, err := s.store.db.ExecContext(ctx, "UPDATE users SET status = 'disabled' WHERE id = ?", userID); err != nil {
		return err
	}
	_, _ = s.store.db.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", userID)
	s.sessions.forgetUser(userID)
	return s.appendAudit(ctx, actor.ID, "user.disabled", "user", userID, "")
}

func (s *Service) ResetCredential(ctx context.Context, actor User, userID, password string) error {
	if actor.Role != RoleAdmin || actor.Status != UserStatusActive {
		return ErrForbidden
	}
	if len([]rune(password)) < 12 {
		return ErrWeakPassword
	}
	var user User
	var privateSpaceID string
	err := s.store.db.QueryRowContext(ctx, `
SELECT u.id, u.username, u.role, u.status, u.created_at, s.id
FROM users u JOIN spaces s ON s.owner_user_id = u.id AND s.kind = 'private'
WHERE u.id = ?`, userID).Scan(&user.ID, &user.Username, &user.Role, &user.Status, new(string), &privateSpaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUserNotFound
	}
	if err != nil {
		return err
	}
	passwordHash, err := hashPassword(password, s.random)
	if err != nil {
		return err
	}
	if s.credentials == nil {
		return errors.New("credential provisioner is unavailable")
	}
	uid, err := s.uidForUser(ctx, user.ID)
	if err != nil {
		return err
	}
	// Resetting a password never re-enables a disabled account.
	if err := s.credentials.SetCredential(ctx, CredentialRequest{
		UserID: user.ID, PrivateSpaceID: privateSpaceID, Username: user.Username, Password: password, Role: user.Role,
		UID: uid, Enabled: user.Status != UserStatusDisabled,
	}); err != nil {
		_, _ = s.store.db.ExecContext(ctx, "UPDATE users SET status = 'error' WHERE id = ? AND status <> 'disabled'", userID)
		_, _ = s.store.db.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", userID)
		s.sessions.forgetUser(userID)
		return fmt.Errorf("provision account credential: %w", err)
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?,
status = CASE WHEN status = 'disabled' THEN 'disabled' ELSE 'active' END WHERE id = ?`, passwordHash, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", userID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.sessions.forgetUser(userID)
	return s.appendAudit(ctx, actor.ID, "user.credential_reset", "user", userID, "")
}

// SyncIdentities asks the Host Agent to converge Linux accounts and group
// membership for every provisioned user. It never changes passwords and is
// safe to call on every Product Service start.
func (s *Service) SyncIdentities(ctx context.Context) error {
	synchronizer, ok := s.credentials.(IdentitySynchronizer)
	if !ok {
		return nil
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT u.username, u.role, u.status, i.uid
FROM users u JOIN linux_identities i ON i.user_id = u.id
WHERE u.status IN ('active', 'disabled') ORDER BY i.uid`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var identities []Identity
	for rows.Next() {
		var identity Identity
		var status UserStatus
		if err := rows.Scan(&identity.Username, &identity.Role, &status, &identity.UID); err != nil {
			return err
		}
		identity.Enabled = status == UserStatusActive
		identities = append(identities, identity)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return synchronizer.SyncIdentities(ctx, identities)
}

func (s *Service) appendAudit(ctx context.Context, actorID, action, resourceType, resourceID, detail string) error {
	_, err := s.store.db.ExecContext(ctx,
		"INSERT INTO audit_events(id, actor_user_id, action, resource_type, resource_id, occurred_at, detail) VALUES(?,?,?,?,?,?,?)",
		s.randomID("audit"), nullableString(actorID), action, resourceType, resourceID, formatTime(s.now().UTC()), detail)
	return err
}

func validUsername(value string) bool {
	return regexp.MustCompile(`^[a-z][a-z0-9_-]{2,31}$`).MatchString(value)
}

func (s *Service) randomID(prefix string) string { return prefix + ":" + s.randomToken(16) }

func (s *Service) randomToken(size int) string {
	value := make([]byte, size)
	if _, err := io.ReadFull(s.random, value); err != nil {
		panic(err)
	}
	return hex.EncodeToString(value)
}

func hashPassword(password string, random io.Reader) (string, error) {
	const memory = 64 * 1024
	const iterations = 3
	const parallelism = 2
	salt := make([]byte, 16)
	if _, err := io.ReadFull(random, salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, 32)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", memory, iterations, parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var memory uint64
	var iterations uint64
	var parallelism uint64
	for _, value := range strings.Split(parts[3], ",") {
		key, raw, found := strings.Cut(value, "=")
		if !found {
			return false
		}
		parsed, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return false
		}
		switch key {
		case "m":
			memory = parsed
		case "t":
			iterations = parsed
		case "p":
			parallelism = parsed
		}
	}
	if memory == 0 || iterations == 0 || parallelism == 0 || parallelism > 255 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, uint32(iterations), uint32(memory), uint8(parallelism), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func tokenHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
