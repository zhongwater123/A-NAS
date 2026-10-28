package accountid

import "testing"

func TestEveryUsernameIsAnAssistantAccount(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]bool{
		"alice": true, "a-nas-photos": true, "app-immich": true, "bob_2": true,
		"al": false, "Alice": false, "1alice": false, "_alice": false, "alice.b": false,
		"abcdefghijklmnopqrstuvwxyz0123456": false,
	} {
		if got := ValidUsername(name); got != want {
			t.Errorf("ValidUsername(%q) = %v, want %v", name, got, want)
		}
		if ValidUsername(name) && !ValidAssistantAccount(name) {
			t.Errorf("username %q is not an assistant account", name)
		}
	}
	if !ValidAssistantAccount("_hermes") || ValidAssistantAccount("") {
		t.Fatal("assistant account rule changed")
	}
}
