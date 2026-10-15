package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/zhongwater123/A-NAS/internal/activity"
)

// How a client reached the Product Service, as recorded with sign-ins.
const (
	ViaLocalConsole = "local-console"
	ViaLAN          = "lan"
	ViaLoopback     = "loopback"
)

// Why a sign-in failed, as recorded in the activity log.
const (
	SignInUnknownUser     = "unknown_user"
	SignInInvalidPassword = "invalid_password"
	SignInDisabled        = "disabled"
	SignInUnavailable     = "unavailable"
)

// ClientOrigin describes where a sign-in or sign-out came from.
type ClientOrigin struct {
	Via     string
	Address string
}

type originKey struct{}

// WithClientOrigin attaches the caller's origin to sign-ins and sign-outs
// made with ctx.
func WithClientOrigin(ctx context.Context, origin ClientOrigin) context.Context {
	return context.WithValue(ctx, originKey{}, origin)
}

func originFrom(ctx context.Context) ClientOrigin {
	origin, _ := ctx.Value(originKey{}).(ClientOrigin)
	return origin
}

// sessionDetail is the JSON detail of session.* events.
type sessionDetail struct {
	Via     string `json:"via,omitempty"`
	Address string `json:"address,omitempty"`
	// Username is the name tried when no account has it.
	Username string `json:"username,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

func (d sessionDetail) String() string {
	encoded, _ := json.Marshal(d)
	return string(encoded)
}

// maxRecordedUsername bounds what a failed sign-in can write into the log.
const maxRecordedUsername = 64

func (s *Service) recordSignIn(ctx context.Context, user User, localConsole bool) {
	origin := originFrom(ctx)
	if localConsole {
		origin.Via = ViaLocalConsole
	}
	_ = s.appendAudit(ctx, user.ID, "session.signed_in", "user", user.ID, sessionDetail{Via: origin.Via, Address: origin.Address}.String())
}

// recordSignInFailure attributes a failed sign-in to the account it named,
// so its owner sees it; attempts on unknown names only reach administrators.
func (s *Service) recordSignInFailure(ctx context.Context, userID, username, reason string) {
	origin := originFrom(ctx)
	detail := sessionDetail{Via: origin.Via, Address: origin.Address, Reason: reason}
	if userID == "" {
		runes := []rune(username)
		if len(runes) > maxRecordedUsername {
			runes = runes[:maxRecordedUsername]
		}
		detail.Username = string(runes)
	}
	_ = s.appendAudit(ctx, userID, "session.sign_in_failed", "user", userID, detail.String())
}

func (s *Service) recordSignOut(ctx context.Context, userID string, localConsole bool) {
	origin := originFrom(ctx)
	if localConsole {
		origin.Via = ViaLocalConsole
	}
	_ = s.appendAudit(ctx, userID, "session.signed_out", "user", userID, sessionDetail{Via: origin.Via, Address: origin.Address}.String())
}

// ActivityCategories are the activity log categories the account audit holds.
var ActivityCategories = []activity.Category{activity.CategorySession, activity.CategoryAccount}

// Activity returns a page of account and sign-in events. Members only ever
// see what they did and what was done to their account or private space,
// whatever q asks for.
func (s *Service) Activity(ctx context.Context, actor User, q activity.Query) ([]activity.Event, error) {
	if actor.Status != UserStatusActive {
		return nil, ErrForbidden
	}
	if actor.Role != RoleAdmin {
		subject, err := s.ActivitySubject(ctx, actor.ID)
		if err != nil {
			return nil, err
		}
		q.Subject = subject
	}
	return activity.Table{DB: s.store.db}.List(ctx, q)
}

// ActivitySubject selects the account events about userID: those the account
// did, and those done to it or to its private space.
func (s *Service) ActivitySubject(ctx context.Context, userID string) (*activity.Subject, error) {
	subject := &activity.Subject{ActorIDs: []string{userID}, ResourceIDs: []string{userID}}
	var spaceID string
	err := s.store.db.QueryRowContext(ctx, "SELECT id FROM spaces WHERE kind = 'private' AND owner_user_id = ?", userID).Scan(&spaceID)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if spaceID != "" {
		subject.ResourceIDs = append(subject.ResourceIDs, spaceID)
	}
	return subject, nil
}

// Directory names the accounts and spaces that activity events refer to,
// including disabled accounts, whose events stay in the log.
type Directory struct {
	Users  map[string]DirectoryUser
	Spaces map[string]Space
}

type DirectoryUser struct {
	Username string
	Nickname string
	Role     Role
}

// Directory reads every account and space; callers decide what to show.
func (s *Service) Directory(ctx context.Context) (Directory, error) {
	directory := Directory{Users: map[string]DirectoryUser{}, Spaces: map[string]Space{}}
	rows, err := s.store.db.QueryContext(ctx, "SELECT id, username, nickname, role FROM users")
	if err != nil {
		return Directory{}, err
	}
	for rows.Next() {
		var id string
		var user DirectoryUser
		if err := rows.Scan(&id, &user.Username, &user.Nickname, &user.Role); err != nil {
			rows.Close()
			return Directory{}, err
		}
		directory.Users[id] = user
	}
	if err := rows.Close(); err != nil {
		return Directory{}, err
	}
	rows, err = s.store.db.QueryContext(ctx, "SELECT id, kind, name, COALESCE(owner_user_id, ''), created_at FROM spaces")
	if err != nil {
		return Directory{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var space Space
		var createdAt string
		if err := rows.Scan(&space.ID, &space.Kind, &space.Name, &space.OwnerUserID, &createdAt); err != nil {
			return Directory{}, err
		}
		space.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		directory.Spaces[space.ID] = space
	}
	return directory, rows.Err()
}
