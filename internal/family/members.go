package family

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// PresetRoles are the family roles offered first; anyone may type another.
var PresetRoles = []string{"爸爸", "妈妈", "爷爷", "奶奶", "外公", "外婆", "哥哥", "姐姐"}

// parentRoles become guardians when they join.
var parentRoles = []string{"爸爸", "妈妈"}

const maxRoleRunes = 8

// Status is where an account stands with the family.
type Status string

const (
	StatusNone    Status = "none"
	StatusPending Status = "pending"
	StatusMember  Status = "member"
)

type Member struct {
	AccountID string    `json:"accountId"`
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	Guardian  bool      `json:"guardian"`
	JoinedAt  time.Time `json:"joinedAt"`
	// Active is false once the account is disabled or gone. Such members
	// keep their content but no longer count as family.
	Active bool `json:"active"`
}

type JoinRequest struct {
	AccountID   string    `json:"accountId"`
	Username    string    `json:"username"`
	Role        string    `json:"role"`
	RequestedAt time.Time `json:"requestedAt"`
}

// Me is the caller's own standing.
type Me struct {
	Status   Status `json:"status"`
	Role     string `json:"role,omitempty"`
	Guardian bool   `json:"guardian"`
}

// Overview is what the family center shows on opening: the caller's
// standing and, for members, the family itself.
type Overview struct {
	Exists bool `json:"exists"`
	Me     Me   `json:"me"`
	// Members, Babies and the rest are empty unless the caller is a member.
	Members  []Member      `json:"members"`
	Requests []JoinRequest `json:"requests"`
	Babies   []Baby        `json:"babies"`
	// DeletedBabies are the babies a guardian can still restore.
	DeletedBabies []Baby `json:"deletedBabies"`
}

func cleanRole(role string) (string, bool) {
	role = strings.TrimSpace(role)
	if role == "" || utf8.RuneCountInString(role) > maxRoleRunes || !utf8.ValidString(role) {
		return "", false
	}
	for _, r := range role {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return role, true
}

// membership is the family as one request sees it: who belongs, with each
// member's account standing settled once.
type membership struct {
	exists  bool
	members []Member
}

func (m membership) member(accountID string) (Member, bool) {
	for _, member := range m.members {
		if member.AccountID == accountID && member.Active {
			return member, true
		}
	}
	return Member{}, false
}

func (m membership) activeGuardians() int {
	count := 0
	for _, member := range m.members {
		if member.Active && member.Guardian {
			count++
		}
	}
	return count
}

func (m membership) activeMembers() int {
	count := 0
	for _, member := range m.members {
		if member.Active {
			count++
		}
	}
	return count
}

// loadMembership reads the family and, when every guardian's account is
// gone or disabled, makes the earliest-joined active member a guardian so
// the family can still approve people and look after its babies.
func (s *Store) loadMembership(ctx context.Context, q queryer) (membership, error) {
	var created string
	err := q.QueryRowContext(ctx, "SELECT created_at FROM family WHERE id = 1").Scan(&created)
	if errors.Is(err, sql.ErrNoRows) {
		return membership{}, nil
	}
	if err != nil {
		return membership{}, err
	}
	accounts, err := s.accounts(ctx)
	if err != nil {
		return membership{}, err
	}
	rows, err := q.QueryContext(ctx,
		"SELECT account_id, username, role, guardian, joined_at FROM members ORDER BY joined_at, account_id")
	if err != nil {
		return membership{}, err
	}
	defer func() { _ = rows.Close() }()
	result := membership{exists: true}
	for rows.Next() {
		var member Member
		var joined string
		if err := rows.Scan(&member.AccountID, &member.Username, &member.Role, &member.Guardian, &joined); err != nil {
			return membership{}, err
		}
		member.JoinedAt = parseTime(joined)
		if account, ok := accounts[member.AccountID]; ok {
			member.Active = true
			member.Username = account.Username
		}
		result.members = append(result.members, member)
	}
	if err := rows.Err(); err != nil {
		return membership{}, err
	}
	if result.activeGuardians() == 0 {
		for i := range result.members {
			if result.members[i].Active {
				if _, err := q.ExecContext(ctx, "UPDATE members SET guardian = 1 WHERE account_id = ?",
					result.members[i].AccountID); err != nil {
					return membership{}, err
				}
				result.members[i].Guardian = true
				break
			}
		}
	}
	return result, nil
}

// requireMember returns the caller's membership or ErrNotMember.
func (s *Store) requireMember(ctx context.Context, q queryer, actor Actor) (membership, Member, error) {
	family, err := s.loadMembership(ctx, q)
	if err != nil {
		return membership{}, Member{}, err
	}
	me, ok := family.member(actor.AccountID)
	if !ok {
		return membership{}, Member{}, ErrNotMember
	}
	return family, me, nil
}

func (s *Store) requireGuardian(ctx context.Context, q queryer, actor Actor) (membership, Member, error) {
	family, me, err := s.requireMember(ctx, q, actor)
	if err == nil && !me.Guardian {
		err = ErrForbidden
	}
	return family, me, err
}

// Overview returns the caller's standing and, for members, the family.
func (s *Store) Overview(ctx context.Context, actor Actor) (Overview, error) {
	var overview Overview
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		family, err := s.loadMembership(ctx, tx)
		if err != nil {
			return err
		}
		overview = Overview{Exists: family.exists, Me: Me{Status: StatusNone},
			Members: []Member{}, Requests: []JoinRequest{}, Babies: []Baby{}, DeletedBabies: []Baby{}}
		me, ok := family.member(actor.AccountID)
		if !ok {
			var role string
			err := tx.QueryRowContext(ctx, "SELECT role FROM join_requests WHERE account_id = ?", actor.AccountID).Scan(&role)
			if err == nil {
				overview.Me = Me{Status: StatusPending, Role: role}
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			return nil
		}
		overview.Me = Me{Status: StatusMember, Role: me.Role, Guardian: me.Guardian}
		for _, member := range family.members {
			if member.Active {
				overview.Members = append(overview.Members, member)
			}
		}
		if me.Guardian {
			if overview.Requests, err = s.joinRequests(ctx, tx); err != nil {
				return err
			}
		}
		if overview.Babies, err = s.babies(ctx, tx, false); err != nil {
			return err
		}
		if me.Guardian {
			if overview.DeletedBabies, err = s.babies(ctx, tx, true); err != nil {
				return err
			}
		}
		return nil
	})
	return overview, err
}

func (s *Store) joinRequests(ctx context.Context, q queryer) ([]JoinRequest, error) {
	accounts, err := s.accounts(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, "SELECT account_id, username, role, requested_at FROM join_requests ORDER BY requested_at")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	requests := []JoinRequest{}
	for rows.Next() {
		var request JoinRequest
		var requested string
		if err := rows.Scan(&request.AccountID, &request.Username, &request.Role, &requested); err != nil {
			return nil, err
		}
		if _, ok := accounts[request.AccountID]; !ok {
			continue
		}
		request.RequestedAt = parseTime(requested)
		requests = append(requests, request)
	}
	return requests, rows.Err()
}

// Join asks to join the family with a role. The first account to join, or
// the first after every member has gone, founds the family as its guardian;
// everyone else waits for a guardian's approval.
func (s *Store) Join(ctx context.Context, actor Actor, role string) (Me, error) {
	role, ok := cleanRole(role)
	if !ok {
		return Me{}, ErrInvalid
	}
	var me Me
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		family, err := s.loadMembership(ctx, tx)
		if err != nil {
			return err
		}
		if _, ok := family.member(actor.AccountID); ok {
			return ErrConflict
		}
		now := formatTime(s.now())
		if family.activeMembers() == 0 {
			if !family.exists {
				if _, err := tx.ExecContext(ctx, "INSERT INTO family(id, created_at, created_by) VALUES(1, ?, ?)", now, actor.AccountID); err != nil {
					return err
				}
			}
			if err := s.addMember(ctx, tx, actor.AccountID, actor.Username, role, true, now, ""); err != nil {
				return err
			}
			me = Me{Status: StatusMember, Role: role, Guardian: true}
			return s.audit(ctx, tx, actor, "family.joined", actor.AccountID, role)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO join_requests(account_id, username, role, requested_at) VALUES(?, ?, ?, ?)
ON CONFLICT(account_id) DO UPDATE SET role = excluded.role, username = excluded.username`,
			actor.AccountID, actor.Username, role, now); err != nil {
			return err
		}
		me = Me{Status: StatusPending, Role: role}
		return s.audit(ctx, tx, actor, "family.join_requested", actor.AccountID, role)
	})
	return me, err
}

func (s *Store) addMember(ctx context.Context, tx *sql.Tx, accountID, username, role string, guardian bool, joined, approvedBy string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM join_requests WHERE account_id = ?", accountID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO members(account_id, username, role, guardian, joined_at, approved_by) VALUES(?, ?, ?, ?, ?, ?)
ON CONFLICT(account_id) DO UPDATE SET username = excluded.username, role = excluded.role, guardian = excluded.guardian,
    joined_at = excluded.joined_at, approved_by = excluded.approved_by`,
		accountID, username, role, guardian, joined, approvedBy)
	return err
}

// CancelJoin withdraws the caller's own request.
func (s *Store) CancelJoin(ctx context.Context, actor Actor) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, "DELETE FROM join_requests WHERE account_id = ?", actor.AccountID)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// Approve admits a requester. Fathers and mothers become guardians.
func (s *Store) Approve(ctx context.Context, actor Actor, accountID string) (Member, error) {
	var member Member
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if _, _, err := s.requireGuardian(ctx, tx, actor); err != nil {
			return err
		}
		var request JoinRequest
		err := tx.QueryRowContext(ctx, "SELECT username, role FROM join_requests WHERE account_id = ?", accountID).
			Scan(&request.Username, &request.Role)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		accounts, err := s.accounts(ctx)
		if err != nil {
			return err
		}
		account, ok := accounts[accountID]
		if !ok {
			return ErrNotFound
		}
		now := s.now()
		guardian := slices.Contains(parentRoles, request.Role)
		if err := s.addMember(ctx, tx, accountID, account.Username, request.Role, guardian, formatTime(now), actor.AccountID); err != nil {
			return err
		}
		member = Member{AccountID: accountID, Username: account.Username, Role: request.Role, Guardian: guardian, JoinedAt: now, Active: true}
		return s.audit(ctx, tx, actor, "family.join_approved", accountID, request.Role)
	})
	return member, err
}

// Reject turns a request down; the account may ask again.
func (s *Store) Reject(ctx context.Context, actor Actor, accountID string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, _, err := s.requireGuardian(ctx, tx, actor); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, "DELETE FROM join_requests WHERE account_id = ?", accountID)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return s.audit(ctx, tx, actor, "family.join_rejected", accountID, "")
	})
}

// SetRole changes the caller's own role.
func (s *Store) SetRole(ctx context.Context, actor Actor, role string) (Member, error) {
	role, ok := cleanRole(role)
	if !ok {
		return Member{}, ErrInvalid
	}
	var member Member
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		_, me, err := s.requireMember(ctx, tx, actor)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE members SET role = ? WHERE account_id = ?", role, actor.AccountID); err != nil {
			return err
		}
		member = me
		member.Role = role
		return s.audit(ctx, tx, actor, "family.role_changed", actor.AccountID, role)
	})
	return member, err
}

// SetGuardian makes a member a guardian or a relative. The family always
// keeps at least one guardian.
func (s *Store) SetGuardian(ctx context.Context, actor Actor, accountID string, guardian bool) (Member, error) {
	var member Member
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		family, _, err := s.requireGuardian(ctx, tx, actor)
		if err != nil {
			return err
		}
		target, ok := family.member(accountID)
		if !ok {
			return ErrNotFound
		}
		if target.Guardian && !guardian && family.activeGuardians() == 1 {
			return ErrLastGuardian
		}
		if _, err := tx.ExecContext(ctx, "UPDATE members SET guardian = ? WHERE account_id = ?", guardian, accountID); err != nil {
			return err
		}
		member = target
		member.Guardian = guardian
		action := "family.guardian_removed"
		if guardian {
			action = "family.guardian_added"
		}
		return s.audit(ctx, tx, actor, action, accountID, "")
	})
	return member, err
}

// Remove takes another member out of the family. What they added stays.
func (s *Store) Remove(ctx context.Context, actor Actor, accountID string) error {
	if accountID == actor.AccountID {
		return ErrInvalid
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		family, _, err := s.requireGuardian(ctx, tx, actor)
		if err != nil {
			return err
		}
		if _, ok := family.member(accountID); !ok {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM members WHERE account_id = ?", accountID); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "family.member_removed", accountID, "")
	})
}

// Leave takes the caller out of the family. The last guardian must first
// hand the role to someone else unless nobody else is left.
func (s *Store) Leave(ctx context.Context, actor Actor) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		family, me, err := s.requireMember(ctx, tx, actor)
		if err != nil {
			return err
		}
		if me.Guardian && family.activeGuardians() == 1 && family.activeMembers() > 1 {
			return ErrLastGuardian
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM members WHERE account_id = ?", actor.AccountID); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "family.left", actor.AccountID, "")
	})
}
