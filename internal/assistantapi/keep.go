package assistantapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// Keeper keeps a member's instance running while the member is signed in,
// so that opening the assistant does not wait for a cold start. The Product
// Service calls Keep when a member signs in or the desktop loads, and Forget
// when they sign out.
type Keeper interface {
	Keep(user accounts.User, sessionToken string)
	Forget(sessionToken string)
}

// The pause before keeping an instance again after its connection ended,
// doubling up to keepRetryMax while the instance keeps failing. Tests shorten
// them.
var (
	keepRetryMin = 2 * time.Second
	keepRetryMax = 2 * time.Minute
	// keepSteady is how long a kept connection lasts before a failure counts
	// as new again.
	keepSteady = time.Minute
)

// keep is one member's kept instance: one idle gateway connection through the
// session gate, made with any of the member's sessions known to be signed
// in. The instance exits only after its last connection has been gone a
// while, so this keeps it from the member's sign-in until their last session
// ends; the gate cuts the connection then (ADR 0024 decision 10).
type keep struct {
	userID   string
	sessions map[string]struct{}
	current  string
	cut      context.CancelFunc
}

// Keep starts keeping the member's instance, or adds a session to the ones it
// is kept with. It returns at once.
func (h *Handler) Keep(user accounts.User, sessionToken string) {
	if h.options.Connector == nil || sessionToken == "" || user.ID == "" || !accountPattern.MatchString(user.Username) || h.ctx.Err() != nil {
		return
	}
	if h.options.SocketDir != "" {
		// Not every member has an assistant on the device.
		if _, err := os.Stat(filepath.Join(h.options.SocketDir, user.Username+".sock")); err != nil {
			return
		}
	}
	h.keepsMu.Lock()
	defer h.keepsMu.Unlock()
	k := h.keeps[user.Username]
	if k == nil {
		k = &keep{userID: user.ID, sessions: map[string]struct{}{}}
		h.keeps[user.Username] = k
		h.background.Go(func() { h.keepInstance(user.Username, k) })
		if h.options.Notices != nil {
			h.background.Go(func() { h.watchTasks(user.Username, k) })
		}
	}
	k.sessions[sessionToken] = struct{}{}
}

// Forget drops a session that ended; when the instance was kept with it, the
// connection moves to another of the member's sessions or closes.
func (h *Handler) Forget(sessionToken string) {
	h.keepsMu.Lock()
	defer h.keepsMu.Unlock()
	for _, k := range h.keeps {
		if _, ok := k.sessions[sessionToken]; !ok {
			continue
		}
		delete(k.sessions, sessionToken)
		if k.current == sessionToken && k.cut != nil {
			k.cut()
		}
	}
}

func (h *Handler) keepInstance(account string, k *keep) {
	delay := keepRetryMin
	for {
		h.keepsMu.Lock()
		session := ""
		for candidate := range k.sessions {
			session = candidate
			break
		}
		if session == "" {
			delete(h.keeps, account)
			h.keepsMu.Unlock()
			return
		}
		ctx, cancel := context.WithCancel(h.ctx)
		k.current, k.cut = session, cancel
		h.keepsMu.Unlock()

		held, err := h.holdInstance(accounts.WithSessionToken(ctx, session), account)
		cancel()

		h.keepsMu.Lock()
		k.current, k.cut = "", nil
		_, signedIn := k.sessions[session]
		switch {
		case errors.Is(err, ErrNoAssistant):
			delete(h.keeps, account)
			h.keepsMu.Unlock()
			return
		case errors.Is(err, ErrSessionRejected):
			// The session ended without a sign-out (expired, or the account
			// was disabled); try the member's other sessions.
			delete(k.sessions, session)
			signedIn = false
		}
		h.keepsMu.Unlock()
		if !signedIn {
			continue
		}
		if held >= keepSteady {
			delay = keepRetryMin
		}
		if err != nil && h.ctx.Err() == nil {
			h.logger.Warn("assistant could not be kept running", "account", account, "error", err, "retry", delay)
		}
		select {
		case <-h.ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, keepRetryMax)
	}
}

// holdInstance opens the instance's gateway and holds it until the
// connection ends, reading and dropping what Hermes sends (it pings, and
// announces itself). It reports how long the connection lasted.
func (h *Handler) holdInstance(ctx context.Context, account string) (time.Duration, error) {
	conn, err := h.dialGateway(ctx, account)
	if err != nil {
		return 0, err
	}
	defer conn.CloseNow()
	started := time.Now()
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			return time.Since(started), nil
		}
	}
}
