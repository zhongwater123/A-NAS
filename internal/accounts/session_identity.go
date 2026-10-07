package accounts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
)

type sessionTokenKey struct{}

// WithSessionToken attaches the caller's session token so the File Broker can
// verify on its own who is asking (ADR 0008).
func WithSessionToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, sessionTokenKey{}, token)
}

// SessionToken returns the token attached by WithSessionToken, or "".
func SessionToken(ctx context.Context) string {
	token, _ := ctx.Value(sessionTokenKey{}).(string)
	return token
}

type rememberedSession struct {
	token     string
	expiresAt time.Time
}

type sessionMemory struct {
	mu     sync.Mutex
	byUser map[string]rememberedSession
}

func (m *sessionMemory) remember(userID, token string, expiresAt time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.byUser == nil {
		m.byUser = make(map[string]rememberedSession)
	}
	if current, ok := m.byUser[userID]; !ok || !current.expiresAt.After(expiresAt) {
		m.byUser[userID] = rememberedSession{token: token, expiresAt: expiresAt}
	}
}

func (m *sessionMemory) forgetUser(userID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.byUser, userID)
}

func (m *sessionMemory) forgetToken(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for userID, remembered := range m.byUser {
		if remembered.token == token {
			delete(m.byUser, userID)
		}
	}
}

func (m *sessionMemory) tokens() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	tokens := make([]string, 0, len(m.byUser))
	for _, remembered := range m.byUser {
		tokens = append(tokens, remembered.token)
	}
	return tokens
}

// RememberedSessions returns, for each user, the newest session this process
// has seen that is still valid. Background work that must act as a user
// through the File Broker can only use these, so a compromised Product Service
// gains no identity it was not already handed by a signed-in user.
func (s *Service) RememberedSessions(ctx context.Context) []Session {
	var sessions []Session
	for _, token := range s.sessions.tokens() {
		session, err := s.ResolveSession(ctx, token)
		if err != nil {
			s.sessions.forgetToken(token)
			continue
		}
		session.Token = token
		sessions = append(sessions, session)
	}
	return sessions
}

// SessionDirectory resolves session tokens to Linux identities for the File
// Broker. The broker runs as root and must not trust the Product Service's
// claim about who is asking, so it reads the session table itself.
type SessionDirectory struct {
	path string
	now  func() time.Time
	mu   sync.Mutex
	db   *sql.DB
}

func NewSessionDirectory(path string) *SessionDirectory {
	return &SessionDirectory{path: path, now: time.Now}
}

// ResolveSessionIdentity returns the identity of an active, unexpired session.
func (d *SessionDirectory) ResolveSessionIdentity(ctx context.Context, token string) (Identity, error) {
	if token == "" {
		return Identity{}, ErrSessionNotFound
	}
	db, err := d.open()
	if err != nil {
		return Identity{}, err
	}
	var identity Identity
	var status UserStatus
	var expiresAt string
	err = db.QueryRowContext(ctx, `
SELECT u.username, u.role, u.status, s.expires_at, i.uid
FROM sessions s
JOIN users u ON u.id = s.user_id
JOIN linux_identities i ON i.user_id = u.id
WHERE s.token_hash = ?`, tokenHash(token)).Scan(&identity.Username, &identity.Role, &status, &expiresAt, &identity.UID)
	if errors.Is(err, sql.ErrNoRows) {
		return Identity{}, ErrSessionNotFound
	}
	if err != nil {
		d.reset()
		return Identity{}, fmt.Errorf("read session directory: %w", err)
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || status != UserStatusActive || !d.now().UTC().Before(expires) {
		return Identity{}, ErrSessionNotFound
	}
	identity.Enabled = true
	return identity, nil
}

// open connects read-only, so the root broker never writes the database or
// creates SQLite side files owned by root next to the Product Service's.
func (d *SessionDirectory) open() (*sql.DB, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.db != nil {
		return d.db, nil
	}
	db, err := sql.Open("sqlite3", "file:"+d.path+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	d.db = db
	return db, nil
}

func (d *SessionDirectory) reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.db != nil {
		_ = d.db.Close()
		d.db = nil
	}
}

func (d *SessionDirectory) Close() error {
	d.reset()
	return nil
}
