package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sessiondir"
)

var (
	ErrReasonRequired    = errors.New("a reason is required to view a member's private space")
	ErrViewingActive     = errors.New("this private space is already being viewed")
	ErrPasswordUnchanged = errors.New("the new password must differ from the current one")
)

// ViewingAccess marks a space the actor sees through Administrative Viewing
// Mode.
type ViewingAccess struct {
	GrantID   string    `json:"grantId"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type ViewingGrant struct {
	ID          string       `json:"id"`
	Scope       ViewingScope `json:"scope"`
	SpaceID     string       `json:"spaceId"`
	OwnerUserID string       `json:"ownerUserId"`
	Reason      string       `json:"reason"`
	GrantedAt   time.Time    `json:"grantedAt"`
	ExpiresAt   time.Time    `json:"expiresAt"`
}

type NotificationKind string

const (
	NotificationAdminViewing        NotificationKind = "admin_viewing"
	NotificationAdminLibraryViewing NotificationKind = "admin_library_viewing"
	NotificationCredentialReset     NotificationKind = "credential_reset"
)

type Notification struct {
	ID            string           `json:"id"`
	Kind          NotificationKind `json:"kind"`
	ActorUsername string           `json:"actorUsername"`
	Reason        string           `json:"reason,omitempty"`
	ExpiresAt     *time.Time       `json:"expiresAt,omitempty"`
	// Task is the run an assistant_task notification tells of.
	Task      *AssistantTaskRun `json:"task,omitempty"`
	CreatedAt time.Time         `json:"createdAt"`
}

type notificationDetail struct {
	Reason    string            `json:"reason,omitempty"`
	ExpiresAt *time.Time        `json:"expiresAt,omitempty"`
	Task      *AssistantTaskRun `json:"task,omitempty"`
}

// StartViewing opens another account's private space read-only to an
// administrator for ViewingDuration. The administrator re-enters their
// password and states a reason; the access is audited and the owner is
// notified.
func (s *Service) StartViewing(ctx context.Context, actor User, ownerUserID, password, reason string) (ViewingGrant, error) {
	reason, spaceID, err := s.prepareViewing(ctx, actor, ownerUserID, password, reason)
	if err != nil {
		return ViewingGrant{}, err
	}
	ownerID := ownerUserID
	if active, err := s.activeGrant(ctx, actor, spaceID); err != nil {
		return ViewingGrant{}, err
	} else if active.ID != "" {
		return ViewingGrant{}, ErrViewingActive
	}
	provisioner, ok := s.credentials.(ViewingProvisioner)
	if !ok {
		return ViewingGrant{}, fmt.Errorf("%w: viewing is unavailable", ErrCredentialProvision)
	}
	uid, err := s.uidForUser(ctx, actor.ID)
	if err != nil {
		return ViewingGrant{}, err
	}
	now := s.now().UTC()
	grant := ViewingGrant{
		ID: s.randomID("viewing"), Scope: ViewingScopeSpace, SpaceID: spaceID, OwnerUserID: ownerID, Reason: reason,
		GrantedAt: now, ExpiresAt: now.Add(ViewingDuration),
	}
	if _, err := s.store.db.ExecContext(ctx, `INSERT INTO viewing_grants
(id, admin_user_id, space_id, owner_user_id, reason, granted_at, expires_at, scope) VALUES(?,?,?,?,?,?,?,?)`,
		grant.ID, actor.ID, spaceID, ownerID, reason, formatTime(now), formatTime(grant.ExpiresAt), ViewingScopeSpace); err != nil {
		return ViewingGrant{}, err
	}
	if err := provisioner.GrantViewing(ctx, ViewingRequest{
		ID: grant.ID, SpaceID: spaceID, AdminUsername: actor.Username, AdminUID: uid, ExpiresAt: grant.ExpiresAt,
	}); err != nil {
		_, _ = s.store.db.ExecContext(ctx, "UPDATE viewing_grants SET ended_at = ? WHERE id = ?", formatTime(now), grant.ID)
		// The Host Agent may have applied the grant after this request gave
		// up waiting; nothing would revoke it before it expires.
		_ = provisioner.RevokeViewing(context.WithoutCancel(ctx), grant.ID)
		if errors.Is(err, ErrVolumeUnavailable) {
			return ViewingGrant{}, err
		}
		return ViewingGrant{}, fmt.Errorf("%w: %v", ErrCredentialProvision, err)
	}
	// A grant nobody was told about must not stay: without the owner's
	// notification and the audit event, the access is withdrawn again.
	detail := encodeDetail(reason, &grant.ExpiresAt)
	if err := s.recordViewingStarted(ctx, nil, actor, ownerID, NotificationAdminViewing, "space.viewing_started", "space", spaceID, detail); err != nil {
		_, _ = s.store.db.ExecContext(ctx, "UPDATE viewing_grants SET ended_at = ? WHERE id = ?", formatTime(now), grant.ID)
		_ = provisioner.RevokeViewing(context.WithoutCancel(ctx), grant.ID)
		return ViewingGrant{}, err
	}
	return grant, nil
}

// StartLibraryViewing opens another account's private photo library
// read-only to an administrator for ViewingDuration, with the same password
// check, reason, audit and owner notification as viewing a private space.
// The photo service enforces it; no ACL changes (ADR 0011).
func (s *Service) StartLibraryViewing(ctx context.Context, actor User, ownerUserID, password, reason string) (ViewingGrant, error) {
	reason, spaceID, err := s.prepareViewing(ctx, actor, ownerUserID, password, reason)
	if err != nil {
		return ViewingGrant{}, err
	}
	now := s.now().UTC()
	var active int
	if err := s.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM viewing_grants
WHERE admin_user_id = ? AND owner_user_id = ? AND scope = ? AND ended_at IS NULL AND expires_at > ?`,
		actor.ID, ownerUserID, ViewingScopeLibrary, formatTime(now)).Scan(&active); err != nil {
		return ViewingGrant{}, err
	}
	if active > 0 {
		return ViewingGrant{}, ErrViewingActive
	}
	grant := ViewingGrant{
		ID: s.randomID("viewing"), Scope: ViewingScopeLibrary, SpaceID: spaceID, OwnerUserID: ownerUserID, Reason: reason,
		GrantedAt: now, ExpiresAt: now.Add(ViewingDuration),
	}
	// The photo service honours the grant as soon as its row exists, so the
	// owner's notification and the audit event commit with it.
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return ViewingGrant{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO viewing_grants
(id, admin_user_id, space_id, owner_user_id, reason, granted_at, expires_at, scope) VALUES(?,?,?,?,?,?,?,?)`,
		grant.ID, actor.ID, spaceID, ownerUserID, reason, formatTime(now), formatTime(grant.ExpiresAt), ViewingScopeLibrary); err != nil {
		return ViewingGrant{}, err
	}
	detail := encodeDetail(reason, &grant.ExpiresAt)
	if err := s.recordViewingStarted(ctx, tx, actor, ownerUserID, NotificationAdminLibraryViewing, "photos.viewing_started", "user", ownerUserID, detail); err != nil {
		return ViewingGrant{}, err
	}
	if err := tx.Commit(); err != nil {
		return ViewingGrant{}, err
	}
	return grant, nil
}

// recordViewingStarted notifies the owner and audits a viewing grant
// together, inside tx or in a transaction of its own when tx is nil.
func (s *Service) recordViewingStarted(ctx context.Context, tx *sql.Tx, actor User, ownerUserID string, kind NotificationKind, action, resourceType, resourceID, detail string) error {
	own := tx == nil
	if own {
		var err error
		if tx, err = s.store.db.BeginTx(ctx, nil); err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
	}
	if err := s.notifyTo(ctx, tx, ownerUserID, kind, actor.Username, detail); err != nil {
		return err
	}
	if err := s.appendAuditTo(ctx, tx, actor.ID, action, resourceType, resourceID, detail); err != nil {
		return err
	}
	if own {
		return tx.Commit()
	}
	return nil
}

// prepareViewing checks what every Administrative Viewing grant requires and
// returns the trimmed reason and the owner's private space.
func (s *Service) prepareViewing(ctx context.Context, actor User, ownerUserID, password, reason string) (string, string, error) {
	if actor.Role != RoleAdmin || actor.Status != UserStatusActive {
		return "", "", ErrForbidden
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len([]rune(reason)) > 500 {
		return "", "", ErrReasonRequired
	}
	var encodedHash string
	if err := s.store.db.QueryRowContext(ctx, "SELECT password_hash FROM users WHERE id = ?", actor.ID).Scan(&encodedHash); err != nil || !verifyPassword(password, encodedHash) {
		return "", "", ErrInvalidCredentials
	}
	if ownerUserID == actor.ID {
		return "", "", ErrForbidden
	}
	var spaceID string
	err := s.store.db.QueryRowContext(ctx, "SELECT id FROM spaces WHERE kind = 'private' AND owner_user_id = ?", ownerUserID).Scan(&spaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrUserNotFound
	}
	return reason, spaceID, err
}

// LibraryViewings lists the actor's unexpired, unended grants to view
// members' private photo libraries.
func (s *Service) LibraryViewings(ctx context.Context, actor User) ([]LibraryViewing, error) {
	if actor.Role != RoleAdmin || actor.Status != UserStatusActive {
		return nil, nil
	}
	return sessiondir.LibraryViewings(ctx, s.store.db, actor.ID, s.now().UTC())
}

// EndViewing revokes an administrator's viewing grant before it expires.
func (s *Service) EndViewing(ctx context.Context, actor User, grantID string) error {
	if actor.Role != RoleAdmin || actor.Status != UserStatusActive {
		return ErrForbidden
	}
	var spaceID, ownerUserID string
	var scope ViewingScope
	err := s.store.db.QueryRowContext(ctx, `SELECT space_id, owner_user_id, scope FROM viewing_grants
WHERE id = ? AND admin_user_id = ? AND ended_at IS NULL`, grantID, actor.ID).Scan(&spaceID, &ownerUserID, &scope)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUserNotFound
	}
	if err != nil {
		return err
	}
	if scope == ViewingScopeLibrary {
		if _, err := s.store.db.ExecContext(ctx, "UPDATE viewing_grants SET ended_at = ? WHERE id = ?", formatTime(s.now().UTC()), grantID); err != nil {
			return err
		}
		return s.appendAudit(ctx, actor.ID, "photos.viewing_ended", "user", ownerUserID, grantID)
	}
	provisioner, ok := s.credentials.(ViewingProvisioner)
	if !ok {
		return fmt.Errorf("%w: viewing is unavailable", ErrCredentialProvision)
	}
	if err := provisioner.RevokeViewing(ctx, grantID); err != nil {
		return fmt.Errorf("%w: %v", ErrCredentialProvision, err)
	}
	if _, err := s.store.db.ExecContext(ctx, "UPDATE viewing_grants SET ended_at = ? WHERE id = ?", formatTime(s.now().UTC()), grantID); err != nil {
		return err
	}
	return s.appendAudit(ctx, actor.ID, "space.viewing_ended", "space", spaceID, grantID)
}

// activeGrant returns the actor's unexpired, unended grant on spaceID, or a
// zero grant.
func (s *Service) activeGrant(ctx context.Context, actor User, spaceID string) (ViewingGrant, error) {
	if actor.Role != RoleAdmin || actor.Status != UserStatusActive {
		return ViewingGrant{}, nil
	}
	var grant ViewingGrant
	var grantedAt, expiresAt string
	err := s.store.db.QueryRowContext(ctx, `SELECT id, space_id, owner_user_id, reason, granted_at, expires_at
FROM viewing_grants WHERE admin_user_id = ? AND space_id = ? AND scope = 'space' AND ended_at IS NULL AND expires_at > ?
ORDER BY expires_at DESC LIMIT 1`, actor.ID, spaceID, formatTime(s.now().UTC())).Scan(
		&grant.ID, &grant.SpaceID, &grant.OwnerUserID, &grant.Reason, &grantedAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ViewingGrant{}, nil
	}
	if err != nil {
		return ViewingGrant{}, err
	}
	grant.GrantedAt, _ = time.Parse(time.RFC3339Nano, grantedAt)
	grant.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expiresAt)
	return grant, nil
}

// viewedSpaces lists the private spaces the actor currently views.
func (s *Service) viewedSpaces(ctx context.Context, actor User) ([]Space, error) {
	if actor.Role != RoleAdmin || actor.Status != UserStatusActive {
		return nil, nil
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT sp.id, sp.kind, sp.name, COALESCE(sp.owner_user_id, ''), sp.created_at, g.id, g.expires_at
FROM viewing_grants g JOIN spaces sp ON sp.id = g.space_id
WHERE g.admin_user_id = ? AND g.scope = 'space' AND g.ended_at IS NULL AND g.expires_at > ?
ORDER BY sp.name`, actor.ID, formatTime(s.now().UTC()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var spaces []Space
	for rows.Next() {
		var space Space
		var createdAt, expiresAt string
		access := ViewingAccess{}
		if err := rows.Scan(&space.ID, &space.Kind, &space.Name, &space.OwnerUserID, &createdAt, &access.GrantID, &expiresAt); err != nil {
			return nil, err
		}
		space.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		access.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expiresAt)
		space.Viewing = &access
		spaces = append(spaces, space)
	}
	return spaces, rows.Err()
}

func encodeDetail(reason string, expiresAt *time.Time) string {
	encoded, _ := json.Marshal(notificationDetail{Reason: reason, ExpiresAt: expiresAt})
	return string(encoded)
}

// notifyTo records a notification through db, which may be a transaction the
// notification must commit with.
func (s *Service) notifyTo(ctx context.Context, db sqlExecutor, userID string, kind NotificationKind, actorUsername, detail string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO notifications(id, user_id, kind, actor_username, detail, created_at)
VALUES(?,?,?,?,?,?)`, s.randomID("notification"), userID, kind, actorUsername, detail, formatTime(s.now().UTC()))
	return err
}

// Notifications returns the actor's unacknowledged notifications, oldest first.
func (s *Service) Notifications(ctx context.Context, actor User) ([]Notification, error) {
	if actor.Status != UserStatusActive {
		return nil, ErrForbidden
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT id, kind, actor_username, detail, created_at FROM notifications
WHERE user_id = ? AND acknowledged_at IS NULL ORDER BY created_at, id`, actor.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	notifications := []Notification{}
	for rows.Next() {
		var notification Notification
		var detail, createdAt string
		if err := rows.Scan(&notification.ID, &notification.Kind, &notification.ActorUsername, &detail, &createdAt); err != nil {
			return nil, err
		}
		notification.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		var decoded notificationDetail
		if detail != "" && json.Unmarshal([]byte(detail), &decoded) == nil {
			notification.Reason = decoded.Reason
			notification.ExpiresAt = decoded.ExpiresAt
			notification.Task = decoded.Task
		}
		notifications = append(notifications, notification)
	}
	return notifications, rows.Err()
}

func (s *Service) AcknowledgeNotification(ctx context.Context, actor User, notificationID string) error {
	if actor.Status != UserStatusActive {
		return ErrForbidden
	}
	result, err := s.store.db.ExecContext(ctx, `UPDATE notifications SET acknowledged_at = ?
WHERE id = ? AND user_id = ? AND acknowledged_at IS NULL`, formatTime(s.now().UTC()), notificationID, actor.ID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return ErrUserNotFound
	}
	return nil
}

// ChangePassword lets a signed-in user replace their own password, which
// also clears a pending forced change and re-enables SMB with the new
// password.
func (s *Service) ChangePassword(ctx context.Context, actor User, currentPassword, newPassword string) error {
	if actor.Status != UserStatusActive {
		return ErrForbidden
	}
	var encodedHash, privateSpaceID string
	var mustChange bool
	err := s.store.db.QueryRowContext(ctx, `SELECT u.password_hash, u.must_change_password, s.id FROM users u
JOIN spaces s ON s.owner_user_id = u.id AND s.kind = 'private' WHERE u.id = ?`, actor.ID).Scan(&encodedHash, &mustChange, &privateSpaceID)
	if err != nil || !verifyPassword(currentPassword, encodedHash) {
		return ErrInvalidCredentials
	}
	if err := checkPassword(newPassword); err != nil {
		return err
	}
	if newPassword == currentPassword {
		return ErrPasswordUnchanged
	}
	passwordHash, err := hashPassword(newPassword, s.random)
	if err != nil {
		return err
	}
	if s.credentials == nil {
		return errors.New("credential provisioner is unavailable")
	}
	uid, err := s.uidForUser(ctx, actor.ID)
	if err != nil {
		return err
	}
	request := CredentialRequest{
		UserID: actor.ID, PrivateSpaceID: privateSpaceID, Username: actor.Username, Password: newPassword,
		Role: actor.Role, UID: uid, Enabled: true,
	}
	if err := s.credentials.SetCredential(ctx, request); err != nil {
		return fmt.Errorf("%w: %v", ErrCredentialProvision, err)
	}
	if err := s.commitPasswordChange(ctx, actor.ID, passwordHash); err != nil {
		// The Web keeps the current password, so SMB goes back to it, disabled
		// again while an administrator's reset still awaits a change.
		request.Password, request.Enabled = currentPassword, !mustChange
		if restoreErr := s.credentials.SetCredential(context.WithoutCancel(ctx), request); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("restore SMB credential: %w", restoreErr))
		}
		return err
	}
	return nil
}

// commitPasswordChange stores a new password hash with its audit event.
func (s *Service) commitPasswordChange(ctx context.Context, userID, passwordHash string) error {
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "UPDATE users SET password_hash = ?, must_change_password = 0 WHERE id = ?", passwordHash, userID); err != nil {
		return err
	}
	if err := s.appendAuditTo(ctx, tx, userID, "user.password_changed", "user", userID, ""); err != nil {
		return err
	}
	return tx.Commit()
}
