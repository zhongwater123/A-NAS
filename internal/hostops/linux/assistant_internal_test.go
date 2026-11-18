package linux

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Hermes's own hash_password with this salt (Python's hashlib.scrypt, n=2**14, r=8, p=1,
// dklen=32): the login of `hermes serve` accepts what the Host Agent writes.
const hermesVector = "scrypt$16384$8$1$AAECAwQFBgcICQoLDA0ODw==$OL7PHos5hOEdPmJED5VMC0CYrthdsc1eXfDp7rbrYLs="

func TestHermesHashIsHermesFormat(t *testing.T) {
	if !hermesHashMatches(hermesVector, "0123456789abcdef") {
		t.Fatal("a hash Hermes made does not verify")
	}
	if hermesHashMatches(hermesVector, "0123456789abcdeF") {
		t.Fatal("a wrong password verifies")
	}
	hash, err := hermesHash("0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Split(hash, "$")
	if len(fields) != 6 || strings.Join(fields[:4], "$") != "scrypt$16384$8$1" || !hermesHashMatches(hash, "0123456789abcdef") {
		t.Fatalf("hash %q", hash)
	}
}

const managedPolicy = `model:
  default: deepseek-flash
mcp_servers:
  anas:
    command: /usr/lib/a-nas/assistant/runtime/venv/bin/python
    # A comment inside the block stays.
    args: [/usr/lib/a-nas/assistant/anas_mcp.py]

  desktop:
    trust: full
# The comment before the next key stays too, as the trial's sed kept it.
skills:
  external_dirs: [/usr/lib/a-nas/assistant/skills]
`

func TestMemberSettings(t *testing.T) {
	root := t.TempDir()
	library := filepath.Join(root, "usr", "lib", "a-nas", "assistant")
	if err := os.MkdirAll(filepath.Join(library, "managed"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, contents := range map[string]string{
		filepath.Join(library, "managed", "config.yaml"): managedPolicy,
		filepath.Join(library, "member-skill.md"):        "You serve {{member}}; {{member}}'s files.\n",
	} {
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	executor := NewExecutor(nil, nil, Options{SystemRoot: root, MountPoint: filepath.Join(t.TempDir(), "data")})
	if err := executor.prepareAssistantSettings("alice"); err != nil {
		t.Fatal(err)
	}
	secret, err := os.ReadFile(executor.assistantTokenPath("alice"))
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(executor.assistantTokenPath("alice")); info.Mode().Perm() != 0o600 || !assistantSecretPattern.Match(secret) {
		t.Fatalf("secret %q mode %v", secret, info.Mode())
	}
	configPath := filepath.Join(executor.assistantMemberDir("alice"), "config.yaml")
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "skills:\n  external_dirs: [/usr/lib/a-nas/assistant/skills, /run/anas-assistant-skills]\n" +
		"mcp_servers:\n  anas:\n    command: /usr/lib/a-nas/assistant/runtime/venv/bin/python\n    # A comment inside the block stays.\n" +
		"    args: [/usr/lib/a-nas/assistant/anas_mcp.py]\n  desktop:\n    trust: full\n" +
		"# The comment before the next key stays too, as the trial's sed kept it.\n" +
		"dashboard:\n  basic_auth:\n    username: alice\n    password_hash: \""
	if !strings.Contains(string(config), want) || strings.Contains(string(config), "deepseek-flash") {
		t.Fatalf("config.yaml:\n%s", config)
	}
	match := hermesHashPattern.FindSubmatch(config)
	password := strings.TrimSpace(strings.TrimPrefix(string(secret), assistantSecret))
	if match == nil || !hermesHashMatches(string(match[1]), password) {
		t.Fatalf("the login does not take the instance's secret:\n%s", config)
	}
	skill, err := os.ReadFile(filepath.Join(executor.assistantMemberDir("alice"), "skills", "anas-member", "SKILL.md"))
	if err != nil || string(skill) != "You serve alice; alice's files.\n" {
		t.Fatalf("skill %q, %v", skill, err)
	}
	// Preparing again keeps the secret and the file a running sandbox bound.
	before, _ := os.Stat(configPath)
	if err := executor.prepareAssistantSettings("alice"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(configPath)
	again, _ := os.ReadFile(executor.assistantTokenPath("alice"))
	if !os.SameFile(before, after) || !bytes.Equal(secret, again) {
		t.Fatal("preparing again replaced the settings")
	}
}
