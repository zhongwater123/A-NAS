package accounts

import (
	"context"
	"sync"
	"time"
)

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
// gains no identity it was not already handed by a signed-in user. A session
// that must change its password is kept but skipped until it has: the broker
// refuses it.
func (s *Service) RememberedSessions(ctx context.Context) []Session {
	var sessions []Session
	for _, token := range s.sessions.tokens() {
		session, err := s.ResolveSession(ctx, token)
		if err != nil {
			s.sessions.forgetToken(token)
			continue
		}
		if session.User.MustChangePassword {
			continue
		}
		session.Token = token
		sessions = append(sessions, session)
	}
	return sessions
}
