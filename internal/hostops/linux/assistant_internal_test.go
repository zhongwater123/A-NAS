package linux

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"
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
    # A comment inside the block.
    args: [/usr/lib/a-nas/assistant/anas_mcp.py]

  desktop:
    trust: full
# The policy's own skills, which the member's settings do not repeat.
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
	// What Hermes reads: the skill folders, the managed policy's MCP servers and nothing else
	// of it, and the login.
	var settings struct {
		Skills struct {
			ExternalDirs []string `yaml:"external_dirs"`
		} `yaml:"skills"`
		MCPServers map[string]map[string]any `yaml:"mcp_servers"`
		Model      any                       `yaml:"model"`
		Dashboard  struct {
			BasicAuth struct {
				Username     string `yaml:"username"`
				PasswordHash string `yaml:"password_hash"`
			} `yaml:"basic_auth"`
		} `yaml:"dashboard"`
	}
	if err := yaml.Unmarshal(config, &settings); err != nil {
		t.Fatalf("config.yaml does not read as YAML: %v\n%s", err, config)
	}
	if !slices.Equal(settings.Skills.ExternalDirs, []string{"/usr/lib/a-nas/assistant/skills", "/run/anas-assistant-skills"}) ||
		settings.MCPServers["anas"]["command"] != "/usr/lib/a-nas/assistant/runtime/venv/bin/python" ||
		settings.MCPServers["desktop"]["trust"] != "full" || settings.Model != nil || settings.Dashboard.BasicAuth.Username != "alice" {
		t.Fatalf("config.yaml:\n%s", config)
	}
	password := strings.TrimSpace(strings.TrimPrefix(string(secret), assistantSecret))
	if !hermesHashMatches(settings.Dashboard.BasicAuth.PasswordHash, password) {
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

// Hermes reads config.yaml as YAML 1.1, where yes, off and the like are booleans and null is
// nothing: account names that are such words must reach its login as the names they are.
func TestMemberNamesThatYAMLReadsAsOtherThingsStayNames(t *testing.T) {
	for _, username := range []string{"yes", "off", "null", "true", "false", "on1", "y-1"} {
		config, err := memberConfig(username, "scrypt$16384$8$1$c2FsdA==$a2V5", []byte(managedPolicy))
		if err != nil {
			t.Fatal(err)
		}
		var line string
		for candidate := range strings.Lines(string(config)) {
			if trimmed := strings.TrimSpace(candidate); strings.HasPrefix(trimmed, "username:") {
				line = trimmed
			}
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "username:"))
		// A plain scalar is read by YAML 1.1's rules; a quoted one is always a string.
		if value != username && value != "'"+username+"'" && value != `"`+username+`"` {
			t.Fatalf("%s: %q", username, line)
		}
		if value == username && slices.Contains([]string{"yes", "off", "null", "true", "false"}, username) {
			t.Fatalf("%s is written plain, which Hermes reads as no name: %q", username, line)
		}
	}
}
