package accountid

import "regexp"

var (
	// usernamePattern is a portable Linux account name.
	usernamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,31}$`)
	// assistantAccountPattern is the account name form the assistant's
	// units accept; it names their sockets and token files (ADR 0024).
	assistantAccountPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
)

// ValidUsername reports a portable Linux account name: a lowercase letter,
// then 2 to 31 lowercase letters, digits, _ or -. The accounts service gives
// members only such names, and the Host Agent provisions only them; an app's
// identity, app-<id>, is one too (ADR 0008).
func ValidUsername(name string) bool { return usernamePattern.MatchString(name) }

// ValidAssistantAccount reports a name the assistant's units accept. Every
// valid username is one.
func ValidAssistantAccount(name string) bool { return assistantAccountPattern.MatchString(name) }
