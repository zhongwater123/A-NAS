// Package accounts owns A-NAS users, credentials, sessions, and space membership.
package accounts

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/argon2"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/activity"
)

var (
	ErrSetupComplete      = errors.New("administrator setup is already complete")
	ErrInvalidUsername    = errors.New("invalid username")
	ErrInvalidPassword    = errors.New("invalid password")
	ErrWeakPassword       = fmt.Errorf("%w: it must contain at least %d characters", ErrInvalidPassword, MinPasswordLength)
	ErrPasswordTooLong    = fmt.Errorf("%w: it must contain at most %d characters", ErrInvalidPassword, MaxPasswordLength)
	ErrPasswordCharacters = fmt.Errorf("%w: it must not contain control characters", ErrInvalidPassword)
	ErrInvalidCredentials = errors.New("invalid username or password")
	// ErrSessionElsewhere reports a valid session presented by another client
	// than the one that signed in (issue #161).
	ErrSessionElsewhere       = errors.New("session was signed in from another client")
	ErrUsernameUnavailable    = errors.New("username is unavailable")
	ErrUsernameReserved       = fmt.Errorf("%w: it is reserved for A-NAS or Samba", ErrUsernameUnavailable)
	ErrCredentialProvision    = errors.New("account credential provisioning failed")
	ErrForbidden              = errors.New("operation is forbidden")
	ErrSpaceNotFound          = errors.New("space not found")
	ErrIdentityRangeExhausted = errors.New("A-NAS Linux identity range is exhausted")
	// ErrSMBDisablePending reports a disabled account whose Web sign-in has
	// ended but whose SMB credential the Host Agent could not disable yet.
	// Identity synchronization disables it when the Product Service starts.
	ErrSMBDisablePending = errors.New("the account is disabled, but its SMB credential could not be disabled yet")
)

// Password rules for every password a user sets. MaxPasswordLength keeps the
// password within what smbpasswd reads as one line (maxPasswordBytes) for any
// text without emoji, and within what SMB clients let users type.
const (
	MinPasswordLength = 12
	MaxPasswordLength = 64
	maxPasswordBytes  = 254
)

type User struct {
	ID        string     `json:"id"`
	Username  string     `json:"username"`
	Role      Role       `json:"role"`
	Status    UserStatus `json:"status"`
	CreatedAt time.Time  `json:"createdAt"`
	// MustChangePassword is set after an administrator resets the password;
	// the user can do nothing else until choosing a new one.
	MustChangePassword bool `json:"mustChangePassword"`
	// Nickname is the name the user chose to be shown by; pages show Username
	// while it is empty. Logins, SMB and Linux keep using Username.
	Nickname string `json:"nickname,omitempty"`
	// AvatarVersion names the user's current avatar image; empty means none.
	AvatarVersion string `json:"avatarVersion,omitempty"`
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
	// Viewing is set when an administrator sees another member's private
	// space through Administrative Viewing Mode; access is read-only.
	Viewing *ViewingAccess `json:"viewing,omitempty"`
}

type Session struct {
	Token     string    `json:"token,omitempty"`
	CSRFToken string    `json:"csrfToken"`
	ExpiresAt time.Time `json:"expiresAt"`
	User      User      `json:"user"`
}

// sessionLifetime bounds sessions signed in from any browser but the local
// console.
const sessionLifetime = 12 * time.Hour

// localConsoleExpiry is stored for local console sessions, which last until
// the user signs out, an administrator resets the password or the account is
// disabled. Every existing expiry check accepts it unchanged.
var localConsoleExpiry = time.Date(9999, time.December, 31, 23, 59, 59, 0, time.UTC)

// LocalConsole reports a session signed in on the device's own screen.
func (s Session) LocalConsole() bool { return s.ExpiresAt.Equal(localConsoleExpiry) }

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
CREATE TABLE IF NOT EXISTS viewing_grants (
    id TEXT PRIMARY KEY,
    admin_user_id TEXT NOT NULL,
    space_id TEXT NOT NULL,
    owner_user_id TEXT NOT NULL,
    reason TEXT NOT NULL,
    granted_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    ended_at TEXT
);
CREATE TABLE IF NOT EXISTS notifications (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    actor_username TEXT NOT NULL DEFAULT '',
    detail TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    acknowledged_at TEXT
);
-- One small, already compressed picture per account (see SetAvatar).
CREATE TABLE IF NOT EXISTS avatars (
    user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    version TEXT NOT NULL,
    content_type TEXT NOT NULL,
    image BLOB NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_user_id ON sessions(user_id);
CREATE INDEX IF NOT EXISTS audit_occurred_at ON audit_events(occurred_at);
CREATE INDEX IF NOT EXISTS audit_action_resource ON audit_events(action, resource_id, occurred_at);
`
	if _, err := s.db.ExecContext(ctx, schema+activity.IndexStatement); err != nil {
		return fmt.Errorf("migrate SQLite: %w", err)
	}
	if err := s.ensureColumn(ctx, "users", "must_change_password", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "users", "nickname", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "users", "avatar_version", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// Grants made before private libraries could be viewed are for spaces.
	if err := s.ensureColumn(ctx, "viewing_grants", "scope", "TEXT NOT NULL DEFAULT 'space'"); err != nil {
		return err
	}
	// The client a session is bound to (ClientOrigin.client). Sessions from
	// before the binding have none and are refused, except local console
	// sessions, which were only ever granted to direct loopback connections.
	if err := s.ensureColumn(ctx, "sessions", "client", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE sessions SET client = ? WHERE client = '' AND expires_at = ?", ViaLoopback, formatTime(localConsoleExpiry)); err != nil {
		return err
	}
	return s.allocateMissingIdentities(ctx)
}

// ensureColumn adds a column that databases created by earlier versions lack.
func (s *Store) ensureColumn(ctx context.Context, table, column, definition string) error {
	rows, err := s.db.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
	return err
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
	if err := checkPassword(password); err != nil {
		return User{}, err
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

// createUser creates an account for the administrator actorID; an empty
// actorID is the device's setup, which creates its first administrator.
func (s *Service) createUser(ctx context.Context, username, password string, role Role, actorID string) (User, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if err := checkUsername(username); err != nil {
		return User{}, err
	}
	if err := checkPassword(password); err != nil {
		return User{}, err
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
	// A new member must not take the name another account is shown by. The
	// device's first account has no one to collide with.
	if actorID != "" {
		if taken, err := nameTaken(ctx, tx, "", username); err != nil {
			return User{}, err
		} else if taken {
			return User{}, ErrUsernameUnavailable
		}
	}
	insert := "INSERT INTO users(id, username, role, status, password_hash, created_at) VALUES(?,?,?,?,?,?)"
	if actorID == "" {
		// Setup found no users before hashing, but so may a concurrent setup:
		// only the insert that still finds none creates the administrator.
		insert = "INSERT INTO users(id, username, role, status, password_hash, created_at) SELECT ?,?,?,?,?,? WHERE NOT EXISTS (SELECT 1 FROM users)"
	}
	result, err := tx.ExecContext(ctx, insert, user.ID, user.Username, user.Role, user.Status, passwordHash, formatTime(now))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return User{}, ErrUsernameUnavailable
		}
		return User{}, err
	}
	if inserted, err := result.RowsAffected(); err != nil {
		return User{}, err
	} else if inserted == 0 {
		return User{}, ErrSetupComplete
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
	return s.authenticate(ctx, username, password, false)
}

// AuthenticateLocalConsole signs in on the device's own screen; the session
// does not expire on its own. Callers must establish that the request really
// comes from the local console.
func (s *Service) AuthenticateLocalConsole(ctx context.Context, username, password string) (Session, error) {
	return s.authenticate(ctx, username, password, true)
}

func (s *Service) authenticate(ctx context.Context, username, password string, localConsole bool) (Session, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	var user User
	var encodedHash, createdAt string
	err := s.store.db.QueryRowContext(ctx,
		"SELECT id, username, role, status, password_hash, created_at, must_change_password, nickname, avatar_version FROM users WHERE username = ?", username,
	).Scan(&user.ID, &user.Username, &user.Role, &user.Status, &encodedHash, &createdAt, &user.MustChangePassword, &user.Nickname, &user.AvatarVersion)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		s.recordSignInFailure(ctx, "", username, SignInUnknownUser)
		return Session{}, ErrInvalidCredentials
	case err != nil:
		return Session{}, ErrInvalidCredentials
	case user.Status == UserStatusDisabled:
		s.recordSignInFailure(ctx, user.ID, username, SignInDisabled)
		return Session{}, ErrInvalidCredentials
	case user.Status != UserStatusActive:
		s.recordSignInFailure(ctx, user.ID, username, SignInUnavailable)
		return Session{}, ErrInvalidCredentials
	case !verifyPassword(password, encodedHash):
		s.recordSignInFailure(ctx, user.ID, username, SignInInvalidPassword)
		return Session{}, ErrInvalidCredentials
	}
	user.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	token := s.randomToken(32)
	csrf := s.randomToken(24)
	now := s.now().UTC()
	expiresAt := now.Add(sessionLifetime)
	if localConsole {
		expiresAt = localConsoleExpiry
	}
	if _, err := s.store.db.ExecContext(ctx,
		"INSERT INTO sessions(token_hash, csrf_token, user_id, expires_at, created_at, client) VALUES(?,?,?,?,?,?)",
		tokenHash(token), csrf, user.ID, formatTime(expiresAt), formatTime(now), originFrom(ctx).client()); err != nil {
		return Session{}, err
	}
	s.sessions.remember(user.ID, token, expiresAt)
	s.recordSignIn(ctx, user, localConsole)
	return Session{Token: token, CSRFToken: csrf, ExpiresAt: expiresAt, User: user}, nil
}

// ResolveSession resolves a token the Product Service already accepted from
// its client, or holds for background work. A token a browser presents goes
// through ResolveClientSession instead.
func (s *Service) ResolveSession(ctx context.Context, token string) (Session, error) {
	session, _, err := s.lookupSession(ctx, token)
	if err != nil {
		return Session{}, err
	}
	s.sessions.remember(session.User.ID, token, session.ExpiresAt)
	return session, nil
}

// ResolveClientSession resolves a token presented by a client, which must be
// the client that signed in. The session cookie also reaches any other port on
// the device's address, such as an installed app's web interface, so on its
// own it must not be enough (issue #161).
func (s *Service) ResolveClientSession(ctx context.Context, token string, origin ClientOrigin) (Session, error) {
	session, client, err := s.lookupSession(ctx, token)
	if err != nil {
		return Session{}, err
	}
	// Sessions from before the binding name no client and must sign in again.
	if client == "" {
		return Session{}, ErrSessionNotFound
	}
	if want := origin.client(); client != want {
		return Session{}, ErrSessionElsewhere
	}
	s.sessions.remember(session.User.ID, token, session.ExpiresAt)
	return session, nil
}

// lookupSession returns an active session and the client it is bound to.
func (s *Service) lookupSession(ctx context.Context, token string) (Session, string, error) {
	var session Session
	var expiresAt, createdAt, client string
	err := s.store.db.QueryRowContext(ctx, `
SELECT s.csrf_token, s.expires_at, s.client, u.id, u.username, u.role, u.status, u.created_at, u.must_change_password, u.nickname, u.avatar_version
FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.token_hash = ?`, tokenHash(token)).Scan(
		&session.CSRFToken, &expiresAt, &client, &session.User.ID, &session.User.Username,
		&session.User.Role, &session.User.Status, &createdAt, &session.User.MustChangePassword,
		&session.User.Nickname, &session.User.AvatarVersion,
	)
	if err != nil {
		return Session{}, "", ErrSessionNotFound
	}
	session.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expiresAt)
	session.User.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	if session.User.Status != UserStatusActive || !s.now().UTC().Before(session.ExpiresAt) {
		_, _ = s.store.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", tokenHash(token))
		s.sessions.forgetToken(token)
		return Session{}, "", ErrSessionNotFound
	}
	return session, client, nil
}

// EndSession signs the session's user out; only the user's own sign-out
// calls it, so it is recorded as one.
func (s *Service) EndSession(ctx context.Context, token string) error {
	s.sessions.forgetToken(token)
	var userID, expiresAt string
	err := s.store.db.QueryRowContext(ctx, "SELECT user_id, expires_at FROM sessions WHERE token_hash = ?", tokenHash(token)).Scan(&userID, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := s.store.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", tokenHash(token)); err != nil {
		return err
	}
	expiry, _ := time.Parse(time.RFC3339Nano, expiresAt)
	s.recordSignOut(ctx, userID, expiry.Equal(localConsoleExpiry))
	return nil
}

func (s *Service) ListUsers(ctx context.Context, actor User) ([]User, error) {
	if actor.Role != RoleAdmin || actor.Status != UserStatusActive {
		return nil, ErrForbidden
	}
	rows, err := s.store.db.QueryContext(ctx, "SELECT "+userColumns+" FROM users ORDER BY username")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (s *Service) ActiveUsers(ctx context.Context) ([]User, error) {
	rows, err := s.store.db.QueryContext(ctx, "SELECT "+userColumns+" FROM users WHERE status = 'active' ORDER BY username")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []User
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	viewing, err := s.viewedSpaces(ctx, actor)
	if err != nil {
		return nil, err
	}
	return append(spaces, viewing...), nil
}

func (s *Service) CanAccessSpace(ctx context.Context, actor User, spaceID string, write bool) (bool, error) {
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
	if kind == SpaceKindShared || (kind == SpaceKindPrivate && ownerID == actor.ID) {
		return true, nil
	}
	if write {
		return false, nil
	}
	grant, err := s.activeGrant(ctx, actor, spaceID)
	return err == nil && grant.ID != "", err
}

// ActiveSpaceIDs lists the shared space and the private spaces of active
// accounts, for work that runs without a signed-in user.
func (s *Service) ActiveSpaceIDs(ctx context.Context) ([]string, error) {
	rows, err := s.store.db.QueryContext(ctx, `SELECT s.id FROM spaces s LEFT JOIN users u ON u.id = s.owner_user_id
WHERE s.kind = ? OR u.status = ? ORDER BY s.id`, SpaceKindShared, UserStatusActive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Space returns one space for work that runs without a signed-in user, such
// as scheduled snapshots; callers authorize the request themselves.
func (s *Service) Space(ctx context.Context, spaceID string) (Space, error) {
	var space Space
	var createdAt string
	err := s.store.db.QueryRowContext(ctx,
		"SELECT id, kind, name, COALESCE(owner_user_id, ''), created_at FROM spaces WHERE id = ?", spaceID,
	).Scan(&space.ID, &space.Kind, &space.Name, &space.OwnerUserID, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Space{}, ErrSpaceNotFound
	}
	if err != nil {
		return Space{}, err
	}
	space.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return space, nil
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
	var status UserStatus
	if err := s.store.db.QueryRowContext(ctx, "SELECT username, status FROM users WHERE id = ?", userID).Scan(&username, &status); errors.Is(err, sql.ErrNoRows) {
		return ErrUserNotFound
	} else if err != nil {
		return err
	}
	if s.credentials == nil {
		return errors.New("credential provisioner is unavailable")
	}
	if status == UserStatusDisabled {
		// Disabling again only retries the SMB credential.
		return s.disableHostCredential(ctx, username)
	}
	// Web access ends first and for good: a Host Agent failure must not leave
	// the account signed in. The status is also what identity
	// synchronization converges the SMB credential to.
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "UPDATE users SET status = 'disabled' WHERE id = ?", userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", userID); err != nil {
		return err
	}
	if err := s.appendAuditTo(ctx, tx, actor.ID, "user.disabled", "user", userID, ""); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.sessions.forgetUser(userID)
	return s.disableHostCredential(ctx, username)
}

func (s *Service) disableHostCredential(ctx context.Context, username string) error {
	if err := s.credentials.DisableCredential(ctx, username); err != nil {
		return fmt.Errorf("%w: %v", ErrSMBDisablePending, err)
	}
	return nil
}

// ResetCredential lets an administrator choose a temporary password for
// another account. Administrators change their own password like everyone
// else, with the current one: a session alone must not be enough to take an
// account over.
func (s *Service) ResetCredential(ctx context.Context, actor User, userID, password string) error {
	if actor.Role != RoleAdmin || actor.Status != UserStatusActive || actor.ID == userID {
		return ErrForbidden
	}
	if err := checkPassword(password); err != nil {
		return err
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
	// Resetting a password never re-enables a disabled account. A member whose
	// password an administrator chose keeps SMB disabled until choosing a new
	// one, so the administrator never knows a usable SMB password.
	if err := s.credentials.SetCredential(ctx, CredentialRequest{
		UserID: user.ID, PrivateSpaceID: privateSpaceID, Username: user.Username, Password: password, Role: user.Role,
		UID: uid, Enabled: false,
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
	if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, must_change_password = 1,
status = CASE WHEN status = 'disabled' THEN 'disabled' ELSE 'active' END WHERE id = ?`, passwordHash, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", userID); err != nil {
		return err
	}
	// The member learns of the reset, and LastCredentialReset shows it, only
	// if both records exist; they commit with the password or not at all.
	if err := s.notifyTo(ctx, tx, userID, NotificationCredentialReset, actor.Username, ""); err != nil {
		return err
	}
	if err := s.appendAuditTo(ctx, tx, actor.ID, "user.credential_reset", "user", userID, ""); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.sessions.forgetUser(userID)
	return nil
}

// CredentialReset is the latest time an administrator chose a user's
// password.
type CredentialReset struct {
	ActorUsername string    `json:"actorUsername"`
	At            time.Time `json:"at"`
}

// LastCredentialReset returns the latest reset of the actor's own password,
// or nil. An administrator can take a member's account over by resetting its
// password and choosing the next one; the member keeps seeing that it
// happened, whoever acknowledged the notification.
func (s *Service) LastCredentialReset(ctx context.Context, actor User) (*CredentialReset, error) {
	var reset CredentialReset
	var at string
	err := s.store.db.QueryRowContext(ctx, `SELECT COALESCE(u.username, ''), a.occurred_at
FROM audit_events a LEFT JOIN users u ON u.id = a.actor_user_id
WHERE a.action = 'user.credential_reset' AND a.resource_id = ? ORDER BY a.occurred_at DESC LIMIT 1`, actor.ID).Scan(&reset.ActorUsername, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	reset.At, _ = time.Parse(time.RFC3339Nano, at)
	return &reset, nil
}

// SyncIdentities asks the Host Agent to converge Linux accounts and group
// membership for every provisioned user. It never changes passwords and is
// safe to call on every Product Service start.
func (s *Service) SyncIdentities(ctx context.Context) error {
	synchronizer, ok := s.credentials.(IdentitySynchronizer)
	if !ok {
		return nil
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT u.username, u.role, u.status, u.must_change_password, i.uid
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
		var mustChange bool
		if err := rows.Scan(&identity.Username, &identity.Role, &status, &mustChange, &identity.UID); err != nil {
			return err
		}
		identity.Enabled = status == UserStatusActive && !mustChange
		identities = append(identities, identity)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return synchronizer.SyncIdentities(ctx, identities)
}

func (s *Service) appendAudit(ctx context.Context, actorID, action, resourceType, resourceID, detail string) error {
	return s.appendAuditTo(ctx, s.store.db, actorID, action, resourceType, resourceID, detail)
}

// appendAuditTo records an audit event through db, which may be a
// transaction the event must commit with.
func (s *Service) appendAuditTo(ctx context.Context, db sqlExecutor, actorID, action, resourceType, resourceID, detail string) error {
	_, err := db.ExecContext(ctx,
		"INSERT INTO audit_events(id, actor_user_id, action, resource_type, resource_id, occurred_at, detail) VALUES(?,?,?,?,?,?,?)",
		s.randomID("audit"), nullableString(actorID), action, resourceType, resourceID, formatTime(s.now().UTC()), detail)
	return err
}

// usernamePattern is a portable Linux account name; the Host Agent accepts the
// same names.
var usernamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,31}$`)

// reservedUsernames are Samba's special sections and the Shared share. Share
// names ignore case, so a member named shared would reach Shared instead of
// their private space over SMB.
var reservedUsernames = []string{"global", "homes", "printers", "shared"}

// checkUsername applies the account name rules to a lowercased name. Names
// starting a-nas or app- belong to accounts A-NAS creates itself, such as
// a-nas-photos and the app identity app-<id> (ADR 0008); a member holding one
// would block that account for good.
func checkUsername(username string) error {
	switch {
	case !usernamePattern.MatchString(username):
		return ErrInvalidUsername
	case slices.Contains(reservedUsernames, username), username == "a-nas",
		strings.HasPrefix(username, "a-nas-"), strings.HasPrefix(username, "app-"):
		return ErrUsernameReserved
	}
	return nil
}

// checkPassword applies the password rules to a password being set. Signing in
// does not, so a password set under earlier rules keeps working. Control
// characters are refused because smbpasswd reads the password as a line of C
// text: a newline or NUL would give SMB a different, shorter password.
func checkPassword(password string) error {
	length := utf8.RuneCountInString(password)
	switch {
	case !utf8.ValidString(password) || strings.ContainsFunc(password, unicode.IsControl):
		return ErrPasswordCharacters
	case length < MinPasswordLength:
		return ErrWeakPassword
	case length > MaxPasswordLength || len(password) > maxPasswordBytes:
		return ErrPasswordTooLong
	}
	return nil
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
	for value := range strings.SplitSeq(parts[3], ",") {
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

func tokenHash(token string) string { return accountid.TokenHash(token) }

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
