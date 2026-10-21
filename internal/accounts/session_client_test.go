package accounts_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// The session cookie also reaches installed apps on other ports of the
// device's address, so a session works only from the client that signed in
// (issue #161).
func TestSessionWorksOnlyFromTheClientThatSignedIn(t *testing.T) {
	ctx := context.Background()
	store, err := accounts.OpenSQLite(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := accounts.NewService(store, &identityRecorder{}, accounts.Options{})
	if _, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	lan := func(address string) accounts.ClientOrigin {
		return accounts.ClientOrigin{Via: accounts.ViaLAN, Address: address}
	}
	loopback := accounts.ClientOrigin{Via: accounts.ViaLoopback}
	signIn := func(origin accounts.ClientOrigin, localConsole bool) string {
		t.Helper()
		signInCtx := accounts.WithClientOrigin(ctx, origin)
		authenticate := service.Authenticate
		if localConsole {
			authenticate = service.AuthenticateLocalConsole
		}
		session, err := authenticate(signInCtx, "owner", "correct horse battery staple")
		if err != nil {
			t.Fatal(err)
		}
		return session.Token
	}

	elsewhere, unbound := accounts.ErrSessionElsewhere, accounts.ErrSessionNotFound
	cases := []struct {
		name  string
		token string
		from  accounts.ClientOrigin
		want  error
	}{
		{"LAN session from its address", signIn(lan("192.0.2.10"), false), lan("192.0.2.10"), nil},
		{"LAN session from an IPv4-mapped form of its address", signIn(lan("192.0.2.10"), false), lan("::ffff:192.0.2.10"), nil},
		{"LAN session replayed by an app container through Caddy", signIn(lan("192.0.2.10"), false), lan("172.18.0.2"), elsewhere},
		{"LAN session from another LAN address", signIn(lan("192.0.2.10"), false), lan("192.0.2.11"), elsewhere},
		{"LAN session over direct loopback", signIn(lan("192.0.2.10"), false), loopback, elsewhere},
		{"LAN session from a request without an address", signIn(lan("192.0.2.10"), false), lan(""), elsewhere},
		{"IPv6 session from another address of its /64", signIn(lan("2001:db8:1:2::10"), false), lan("2001:db8:1:2:aa::99"), nil},
		{"IPv6 session from another /64", signIn(lan("2001:db8:1:2::10"), false), lan("2001:db8:1:3::10"), elsewhere},
		{"link-local session from its address", signIn(lan("fe80::10%eth0"), false), lan("fe80::10%eth0"), nil},
		{"link-local session from another link-local address", signIn(lan("fe80::10%eth0"), false), lan("fe80::42:acff:fe12:2%eth0"), elsewhere},
		{"local console session on the device", signIn(loopback, true), loopback, nil},
		{"local console session through Caddy", signIn(loopback, true), lan("127.0.0.1"), elsewhere},
		{"session signed in without an address", signIn(lan("unknown"), false), lan("unknown"), unbound},
		{"session signed in without an origin", signIn(accounts.ClientOrigin{}, false), accounts.ClientOrigin{}, unbound},
	}
	for _, c := range cases {
		session, err := service.ResolveClientSession(ctx, c.token, c.from)
		switch {
		case c.want == nil && (err != nil || session.User.Username != "owner"):
			t.Errorf("%s: ResolveClientSession() = %#v, %v, want the owner's session", c.name, session, err)
		case c.want != nil && !errors.Is(err, c.want):
			t.Errorf("%s: ResolveClientSession() error = %v, want %v", c.name, err, c.want)
		}
	}
	if _, err := service.ResolveClientSession(ctx, "not-a-session-token", loopback); !errors.Is(err, accounts.ErrSessionNotFound) {
		t.Errorf("forged token error = %v, want ErrSessionNotFound", err)
	}
	// Tokens the Product Service already accepted resolve wherever it uses them.
	if _, err := service.ResolveSession(ctx, signIn(lan("192.0.2.10"), false)); err != nil {
		t.Errorf("ResolveSession() error = %v", err)
	}
}

// Sessions signed in before the binding existed name no client. Local console
// sessions were only granted to direct loopback connections and stay signed
// in there; the others must sign in again.
func TestSessionsFromBeforeTheBindingAreBoundOrRefused(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	store, err := accounts.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	service := accounts.NewService(store, &identityRecorder{}, accounts.Options{})
	if _, err := service.SetupAdministrator(ctx, "owner", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	console, err := service.AuthenticateLocalConsole(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	browser, err := service.Authenticate(ctx, "owner", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE sessions SET client = ''"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = accounts.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service = accounts.NewService(store, &identityRecorder{}, accounts.Options{})
	if _, err := service.ResolveClientSession(ctx, console.Token, accounts.ClientOrigin{Via: accounts.ViaLoopback}); err != nil {
		t.Errorf("earlier local console session on the device: %v", err)
	}
	if _, err := service.ResolveClientSession(ctx, console.Token, accounts.ClientOrigin{Via: accounts.ViaLAN, Address: "192.0.2.10"}); !errors.Is(err, accounts.ErrSessionElsewhere) {
		t.Errorf("earlier local console session through Caddy error = %v, want ErrSessionElsewhere", err)
	}
	for _, origin := range []accounts.ClientOrigin{{Via: accounts.ViaLoopback}, {Via: accounts.ViaLAN, Address: "192.0.2.10"}} {
		if _, err := service.ResolveClientSession(ctx, browser.Token, origin); !errors.Is(err, accounts.ErrSessionNotFound) {
			t.Errorf("earlier browser session from %#v error = %v, want ErrSessionNotFound", origin, err)
		}
	}
}
