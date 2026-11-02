// Package companion keeps each member's small shared activity and a fenced
// interaction lease. It has no file, photo, model or privileged operations.
package companion

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

const LeaseDuration = 15 * time.Second

var (
	ErrInvalid  = errors.New("invalid companion command")
	ErrConflict = errors.New("companion state changed or interaction lease was lost")
)

type Lease struct {
	ClientID    string `json:"clientId"`
	Kind        string `json:"kind"`
	Epoch       int64  `json:"epoch"`
	RemainingMS int64  `json:"remainingMs"`
}
type Pending struct {
	ClientID string `json:"clientId"`
	Kind     string `json:"kind"`
}
type State struct {
	Revision       int64    `json:"revision"`
	Character      string   `json:"character"`
	Quiet          bool     `json:"quiet"`
	PlayCount      int64    `json:"playCount"`
	ConversationID string   `json:"conversationId"`
	Active         *Lease   `json:"active"`
	Pending        *Pending `json:"pending"`
}
type Claim struct {
	ClientID string `json:"clientId"`
	Kind     string `json:"kind"`
	Revision int64  `json:"revision"`
}
type Holder struct {
	ClientID string `json:"clientId"`
	Epoch    int64  `json:"epoch"`
}
type Action struct {
	ClientID       string `json:"clientId"`
	Epoch          int64  `json:"epoch"`
	EventID        string `json:"eventId"`
	Type           string `json:"type"`
	Character      string `json:"character,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
}
type Options struct{ Now func() time.Time }
type Store struct {
	db  *sql.DB
	now func() time.Time
}
type record struct {
	State
	client, kind, pendingClient, pendingKind string
	epoch, until                             int64
}

func Open(path string, options Options) (*Store, error) {
	db, err := sqlitedb.Open(path, sqlitedb.Options{})
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS companions (
owner TEXT PRIMARY KEY, revision INTEGER NOT NULL, character TEXT NOT NULL, quiet INTEGER NOT NULL,
play_count INTEGER NOT NULL, client TEXT NOT NULL, kind TEXT NOT NULL, epoch INTEGER NOT NULL,
lease_until INTEGER NOT NULL, pending_client TEXT NOT NULL, pending_kind TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS companion_events (
owner TEXT NOT NULL, event_id TEXT NOT NULL, digest TEXT NOT NULL, revision INTEGER NOT NULL,
PRIMARY KEY(owner, event_id));`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	// This isolated database originally had no schema version. Adding a task
	// reference keeps existing activity and does not copy any transcript.
	tx, err := db.Begin()
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	var version int
	if err = tx.QueryRow("PRAGMA user_version").Scan(&version); err == nil {
		switch version {
		case 0:
			_, err = tx.Exec("ALTER TABLE companions ADD COLUMN conversation_id TEXT NOT NULL DEFAULT ''; PRAGMA user_version = 1")
		case 1:
		default:
			err = fmt.Errorf("unsupported companion database version %d", version)
		}
	}
	if err == nil {
		err = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	if err != nil {
		_ = tx.Rollback()
		_ = db.Close()
		return nil, err
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Store{db: db, now: options.Now}, nil
}
func (s *Store) Close() error { return s.db.Close() }

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func load(ctx context.Context, db queryer, owner string) (record, error) {
	r := record{State: State{Character: "naiwa"}}
	err := db.QueryRowContext(ctx, `SELECT revision, character, quiet, play_count, client, kind, epoch,
lease_until, pending_client, pending_kind, conversation_id FROM companions WHERE owner = ?`, owner).
		Scan(&r.Revision, &r.Character, &r.Quiet, &r.PlayCount, &r.client, &r.kind, &r.epoch,
			&r.until, &r.pendingClient, &r.pendingKind, &r.ConversationID)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return r, err
}
func (r record) view(now time.Time) State {
	v := r.State
	if r.client != "" && r.until > now.UnixMilli() {
		v.Active = &Lease{ClientID: r.client, Kind: r.kind, Epoch: r.epoch, RemainingMS: r.until - now.UnixMilli()}
	}
	if r.pendingClient != "" {
		v.Pending = &Pending{ClientID: r.pendingClient, Kind: r.pendingKind}
	}
	return v
}
func (s *Store) Read(ctx context.Context, owner string) (State, error) {
	if owner == "" {
		return State{}, ErrInvalid
	}
	r, err := load(ctx, s.db, owner)
	return r.view(s.now()), err
}

func save(ctx context.Context, tx *sql.Tx, owner string, r record) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO companions VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(owner) DO UPDATE SET revision=excluded.revision, character=excluded.character, quiet=excluded.quiet,
play_count=excluded.play_count, client=excluded.client, kind=excluded.kind, epoch=excluded.epoch,
lease_until=excluded.lease_until, pending_client=excluded.pending_client, pending_kind=excluded.pending_kind, conversation_id=excluded.conversation_id`,
		owner, r.Revision, r.Character, r.Quiet, r.PlayCount, r.client, r.kind, r.epoch, r.until, r.pendingClient, r.pendingKind, r.ConversationID)
	return err
}
func (s *Store) change(ctx context.Context, owner string, change func(*sql.Tx, *record, time.Time) error) (State, error) {
	if owner == "" {
		return State{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return State{}, err
	}
	defer func() { _ = tx.Rollback() }()
	r, err := load(ctx, tx, owner)
	if err != nil {
		return State{}, err
	}
	now := s.now()
	if err = change(tx, &r, now); err != nil {
		return State{}, err
	}
	if err = save(ctx, tx, owner, r); err != nil {
		return State{}, err
	}
	if err = tx.Commit(); err != nil {
		return State{}, err
	}
	return r.view(now), nil
}

var conversationID = regexp.MustCompile(`^[0-9]{8}_[0-9]{6}_[0-9a-f]{6}$`)

func validClient(id string) bool { _, err := uuid.Parse(id); return err == nil }
func (r *record) grant(client, kind string, now time.Time) {
	r.client, r.kind, r.pendingClient, r.pendingKind = client, kind, "", ""
	r.epoch++
	r.until = now.Add(LeaseDuration).UnixMilli()
}
func (s *Store) Claim(ctx context.Context, owner string, command Claim) (State, error) {
	if !validClient(command.ClientID) || (command.Kind != "web" && command.Kind != "desktop") || command.Revision < 0 {
		return State{}, ErrInvalid
	}
	return s.change(ctx, owner, func(_ *sql.Tx, r *record, now time.Time) error {
		if r.Revision != command.Revision {
			return ErrConflict
		}
		switch {
		case r.client == command.ClientID && r.until > now.UnixMilli():
			// Explicit recall by the current holder also cancels a pending transfer.
			r.grant(command.ClientID, command.Kind, now)
		case r.client == "" || r.until <= now.UnixMilli():
			r.grant(command.ClientID, command.Kind, now)
		case r.pendingClient == command.ClientID && r.pendingKind == command.Kind:
			return nil
		default:
			// The new client waits. The old client first stops locally, then
			// releases; a disconnected old client must let its lease expire.
			r.pendingClient, r.pendingKind = command.ClientID, command.Kind
		}
		r.Revision++
		return nil
	})
}
func owns(r *record, holder Holder, now time.Time) bool {
	return r.client == holder.ClientID && r.epoch == holder.Epoch && r.until > now.UnixMilli()
}
func (s *Store) Heartbeat(ctx context.Context, owner string, holder Holder) (State, error) {
	if !validClient(holder.ClientID) || holder.Epoch < 1 {
		return State{}, ErrInvalid
	}
	return s.change(ctx, owner, func(_ *sql.Tx, r *record, now time.Time) error {
		if !owns(r, holder, now) {
			return ErrConflict
		}
		if r.pendingClient == "" {
			r.until = now.Add(LeaseDuration).UnixMilli()
		}
		return nil
	})
}
func (s *Store) Release(ctx context.Context, owner string, holder Holder) (State, error) {
	if !validClient(holder.ClientID) || holder.Epoch < 1 {
		return State{}, ErrInvalid
	}
	return s.change(ctx, owner, func(_ *sql.Tx, r *record, now time.Time) error {
		if !owns(r, holder, now) {
			return ErrConflict
		}
		if r.pendingClient != "" {
			r.grant(r.pendingClient, r.pendingKind, now)
		} else {
			r.client, r.kind, r.until = "", "", 0
			r.epoch++
		}
		r.Revision++
		return nil
	})
}
func (s *Store) Act(ctx context.Context, owner string, action Action) (State, error) {
	if !validClient(action.ClientID) || !validClient(action.EventID) || action.Epoch < 1 {
		return State{}, ErrInvalid
	}
	if action.Type != "conversation" && action.ConversationID != "" {
		return State{}, ErrInvalid
	}
	switch action.Type {
	case "conversation":
		if action.Character != "" || (action.ConversationID != "" && !conversationID.MatchString(action.ConversationID)) {
			return State{}, ErrInvalid
		}
	case "ball", "quiet", "wake":
		if action.Character != "" {
			return State{}, ErrInvalid
		}
	case "character":
		if action.Character != "naiwa" && action.Character != "glow" && action.Character != "chestnut" {
			return State{}, ErrInvalid
		}
	default:
		return State{}, ErrInvalid
	}
	encoded, _ := json.Marshal(action)
	digest := fmt.Sprintf("%x", sha256.Sum256(encoded))
	return s.change(ctx, owner, func(tx *sql.Tx, r *record, now time.Time) error {
		var previous string
		err := tx.QueryRowContext(ctx, "SELECT digest FROM companion_events WHERE owner=? AND event_id=?", owner, action.EventID).Scan(&previous)
		if err == nil {
			if previous != digest {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if !owns(r, Holder{action.ClientID, action.Epoch}, now) || r.pendingClient != "" {
			return ErrConflict
		}
		switch action.Type {
		case "conversation":
			r.ConversationID = action.ConversationID
		case "ball":
			if r.PlayCount < 1_000_000_000 {
				r.PlayCount++
			}
		case "quiet":
			r.Quiet = true
		case "wake":
			r.Quiet = false
		case "character":
			r.Character = action.Character
		}
		r.Revision++
		if _, err = tx.ExecContext(ctx, "INSERT INTO companion_events VALUES(?, ?, ?, ?)", owner, action.EventID, digest, r.Revision); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM companion_events WHERE owner=? AND event_id NOT IN
(SELECT event_id FROM companion_events WHERE owner=? ORDER BY revision DESC LIMIT 256)`, owner, owner)
		return err
	})
}
