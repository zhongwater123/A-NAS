package accounts_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

func TestNewAccountNamesFollowTheNameRules(t *testing.T) {
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "product.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	credentials := &credentialRecorder{}
	service := accounts.NewService(store, credentials, accounts.Options{})
	admin, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]error{
		" Carol ":               nil,
		"bob_2":                 nil,
		"anas":                  nil,
		"applepie":              nil,
		"ab":                    accounts.ErrInvalidUsername,
		"2bob":                  accounts.ErrInvalidUsername,
		"-bob":                  accounts.ErrInvalidUsername,
		"小明":                    accounts.ErrInvalidUsername,
		"zhang san":             accounts.ErrInvalidUsername,
		"machine$":              accounts.ErrInvalidUsername,
		strings.Repeat("a", 33): accounts.ErrInvalidUsername,
		"Shared":                accounts.ErrUsernameReserved,
		"homes":                 accounts.ErrUsernameReserved,
		"global":                accounts.ErrUsernameReserved,
		"printers":              accounts.ErrUsernameReserved,
		"a-nas":                 accounts.ErrUsernameReserved,
		"a-nas-photos":          accounts.ErrUsernameReserved,
		"app-memos":             accounts.ErrUsernameReserved,
	} {
		credentials.username = ""
		_, err := service.CreateMember(ctx, admin, name, "member password for testing")
		if !errors.Is(err, want) || (want == nil) != (err == nil) {
			t.Errorf("CreateMember(%q) error = %v, want %v", name, err, want)
		}
		if want != nil && credentials.username != "" {
			t.Errorf("CreateMember(%q) provisioned host account %q", name, credentials.username)
		}
	}
	if !errors.Is(accounts.ErrUsernameReserved, accounts.ErrUsernameUnavailable) {
		t.Fatal("a reserved name is not reported as unavailable")
	}
	// Refused names take no UID: four accounts were created after the owner.
	if _, err := service.CreateMember(ctx, admin, "dave", "member password for testing"); err != nil || credentials.uid != accounts.FirstUserUID+5 {
		t.Fatalf("CreateMember(dave) UID = %d, %v; want %d", credentials.uid, err, accounts.FirstUserUID+5)
	}
}

func TestPasswordsBeingSetFollowThePasswordRules(t *testing.T) {
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "product.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := accounts.NewService(store, &credentialRecorder{}, accounts.Options{})
	admin, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	const initial = "member password for testing"
	for index, test := range []struct {
		password string
		want     error
	}{
		{"twelve chars", nil},
		{"  spaces count too  ", nil},
		{"十二个汉字也可以作为一个密码", nil},
		{strings.Repeat("x", 64), nil},
		{strings.Repeat("😀", 12), nil},
		{"eleven char", accounts.ErrWeakPassword},
		{strings.Repeat("x", 65), accounts.ErrPasswordTooLong},
		{strings.Repeat("😀", 64), accounts.ErrPasswordTooLong},
		{"short\nshort\nthen any text", accounts.ErrPasswordCharacters},
		{"short\x00then any text", accounts.ErrPasswordCharacters},
		{"tab\tseparated text", accounts.ErrPasswordCharacters},
		{"invalid \xff UTF-8 text here", accounts.ErrPasswordCharacters},
	} {
		check := func(operation string, err error) {
			t.Helper()
			if !errors.Is(err, test.want) || (test.want == nil) != (err == nil) || (test.want != nil && !errors.Is(err, accounts.ErrInvalidPassword)) {
				t.Errorf("%s(%q) error = %v, want %v", operation, test.password, err, test.want)
			}
		}
		_, err := service.CreateMember(ctx, admin, fmt.Sprintf("created%02d", index), test.password)
		check("CreateMember", err)
		reset, err := service.CreateMember(ctx, admin, fmt.Sprintf("reset%02d", index), initial)
		if err != nil {
			t.Fatal(err)
		}
		check("ResetCredential", service.ResetCredential(ctx, admin, reset.ID, test.password))
		changed, err := service.CreateMember(ctx, admin, fmt.Sprintf("changed%02d", index), initial)
		if err != nil {
			t.Fatal(err)
		}
		check("ChangePassword", service.ChangePassword(ctx, changed, initial, test.password))
	}
}

func TestAdministratorsChangeTheirOwnPasswordOnlyWithTheCurrentOne(t *testing.T) {
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "product.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := accounts.NewService(store, &credentialRecorder{}, accounts.Options{})
	admin, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ResetCredential(ctx, admin, admin.ID, "administrator new password"); !errors.Is(err, accounts.ErrForbidden) {
		t.Fatalf("ResetCredential(self) error = %v, want ErrForbidden", err)
	}
	if _, err := service.Authenticate(ctx, "owner", "correct horse battery staple"); err != nil {
		t.Fatalf("refused self-reset changed the password: %v", err)
	}
	if err := service.ChangePassword(ctx, admin, "correct horse battery staple", "administrator new password"); err != nil {
		t.Fatalf("ChangePassword() error = %v", err)
	}
}

// setupBarrier holds the first two random reads, the password salts, until
// both setups have read an empty user table.
type setupBarrier struct {
	reads atomic.Int32
	ready chan struct{}
}

func (b *setupBarrier) Read(p []byte) (int, error) {
	n := b.reads.Add(1)
	if n == 2 {
		close(b.ready)
	}
	if n <= 2 {
		select {
		case <-b.ready:
		case <-time.After(5 * time.Second):
			return 0, errors.New("the other setup never arrived")
		}
	}
	return rand.Read(p)
}

func TestConcurrentSetupsCreateOneAdministrator(t *testing.T) {
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "product.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := accounts.NewService(store, &credentialRecorder{}, accounts.Options{Random: &setupBarrier{ready: make(chan struct{})}})
	type result struct {
		user accounts.User
		err  error
	}
	results := make(chan result, 2)
	for _, name := range []string{"first", "second"} {
		go func() {
			user, err := service.SetupAdministrator(ctx, name, "correct horse battery staple")
			results <- result{user, err}
		}()
	}
	a, b := <-results, <-results
	if (a.err == nil) == (b.err == nil) {
		t.Fatalf("setup results = %v, %v; want exactly one success", a.err, b.err)
	}
	winner, loser := a, b
	if a.err != nil {
		winner, loser = b, a
	}
	if !errors.Is(loser.err, accounts.ErrSetupComplete) {
		t.Fatalf("losing setup error = %v, want ErrSetupComplete", loser.err)
	}
	users, err := service.ListUsers(ctx, winner.user)
	if err != nil || len(users) != 1 || users[0].ID != winner.user.ID {
		t.Fatalf("users = %#v, %v; want only the winning administrator", users, err)
	}
}
