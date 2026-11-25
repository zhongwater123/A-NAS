package workspace

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accountid"
)

// TransferLife is how long a one-time transfer link may wait for its use.
const TransferLife = 10 * time.Minute

// Direction is which way a one-time transfer moves a page.
type Direction string

const (
	Download Direction = "download"
	Upload   Direction = "upload"
)

// Transfer is a one-time link an agent uses with curl, so that a whole page
// does not pass through its conversation (ADR 0040 decision 6). It is bound
// to the account, the page, the version and the agent token that asked for
// it; the token is checked again when the link is used.
type Transfer struct {
	Direction Direction
	// PageID is empty for an upload that makes a new page.
	PageID string
	// Version is what a download serves (0 for the latest) or what an upload
	// replaces.
	Version int
	Actor   Actor
	Via     Via
	Note    string
	Expires time.Time
}

// CreateTransfer stores a one-time link and returns its key, which is not
// kept.
func (s *Store) CreateTransfer(ctx context.Context, transfer Transfer) (string, time.Time, error) {
	if transfer.Via.TokenID == "" {
		return "", time.Time{}, fmt.Errorf("%w: only an agent's call makes a transfer link", ErrInvalid)
	}
	if transfer.Direction != Download && transfer.Direction != Upload {
		return "", time.Time{}, fmt.Errorf("%w: unknown direction %q", ErrInvalid, transfer.Direction)
	}
	if transfer.PageID != "" {
		if _, _, err := s.Version(ctx, transfer.PageID, 0); err != nil {
			return "", time.Time{}, err
		}
	} else if transfer.Direction == Download {
		return "", time.Time{}, fmt.Errorf("%w: name the page to download", ErrInvalid)
	}
	note, err := cleanNote(transfer.Note)
	if err != nil {
		return "", time.Time{}, err
	}
	key := rand.Text()
	expires := s.now().Add(TransferLife)
	_, err = s.db.ExecContext(ctx, `INSERT INTO transfers(key_hash, direction, page_id, version, account_id, account_name, token_id, via_kind, via_name, note, expires_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`, accountid.TokenHash(key), string(transfer.Direction), transfer.PageID, transfer.Version, transfer.Actor.AccountID,
		transfer.Actor.Name, transfer.Via.TokenID, string(transfer.Via.Kind), transfer.Via.Name, note, formatTime(expires))
	if err != nil {
		return "", time.Time{}, err
	}
	return key, expires, nil
}

// TakeTransfer uses up the link with key: it answers ErrNotFound for a key
// that is unknown, used or expired. The caller then checks the token again
// before doing what the link says.
func (s *Store) TakeTransfer(ctx context.Context, key string, direction Direction) (Transfer, error) {
	var transfer Transfer
	var kind, expires string
	err := s.db.QueryRowContext(ctx, `DELETE FROM transfers WHERE key_hash = ? AND direction = ?
RETURNING page_id, version, account_id, account_name, token_id, via_kind, via_name, note, expires_at`, accountid.TokenHash(key), string(direction)).
		Scan(&transfer.PageID, &transfer.Version, &transfer.Actor.AccountID, &transfer.Actor.Name, &transfer.Via.TokenID, &kind, &transfer.Via.Name, &transfer.Note, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Transfer{}, ErrNotFound
	}
	if err != nil {
		return Transfer{}, err
	}
	transfer.Direction, transfer.Via.Kind, transfer.Expires = direction, ViaKind(kind), parseTime(expires)
	if !s.now().Before(transfer.Expires) {
		return Transfer{}, ErrNotFound
	}
	return transfer, nil
}
