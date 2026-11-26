package linux

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	// Without a policy for planning, the planning instance's settings are the instance's.
	planPath := filepath.Join(executor.assistantMemberDir("alice"), "config-plan.yaml")
	if plan, err := os.ReadFile(planPath); err != nil || string(plan) != string(config) {
		t.Fatalf("config-plan.yaml %q, %v", plan, err)
	}
	// With one, they add its servers, VideoCut's planning server: only the planning
	// instance binds them (docs/architecture/montage-planning-session.md). The settings the
	// scheduled tasks once had for it go.
	if err := os.MkdirAll(filepath.Join(library, "managed-plan"), 0o755); err != nil {
		t.Fatal(err)
	}
	planPolicy := strings.Replace(managedPolicy, "mcp_servers:\n", "mcp_servers:\n  videocut:\n    trust: full\n", 1)
	if err := os.WriteFile(filepath.Join(library, "managed-plan", "config.yaml"), []byte(planPolicy), 0o644); err != nil {
		t.Fatal(err)
	}
	cronPath := filepath.Join(executor.assistantMemberDir("alice"), "config-cron.yaml")
	if err := os.WriteFile(cronPath, []byte(planPolicy), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := executor.prepareAssistantSettings("alice"); err != nil {
		t.Fatal(err)
	}
	var planSettings struct {
		MCPServers map[string]map[string]any `yaml:"mcp_servers"`
	}
	plan, err := os.ReadFile(planPath)
	if err != nil || yaml.Unmarshal(plan, &planSettings) != nil || planSettings.MCPServers["videocut"]["trust"] != "full" ||
		planSettings.MCPServers["anas"] == nil {
		t.Fatalf("config-plan.yaml:\n%s", plan)
	}
	if _, err := os.Stat(cronPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config-cron.yaml is still there: %v", err)
	}
	if again, _ := os.ReadFile(configPath); strings.Contains(string(again), "videocut") {
		t.Fatalf("the instance's config.yaml has the videocut server:\n%s", again)
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

// The policy of a member's planning instance is the assistant's policy with VideoCut's
// planning server and a review after every planning, and nothing else changed
// (docs/architecture/montage-planning-session.md). The package makes it by splicing text
// (scripts/lib/assistant-units.sh), which no check of the text shows to be the YAML it should
// be: here the package's own function makes it from the files the package ships, and it is read
// as the Host Agent reads it.
func TestThePlanPolicyIsThePolicyWithTheVideoCutServerAndReviews(t *testing.T) {
	// The root integration tests run this package's test binary in a container
	// that has no checkout (scripts/root-integration-test.sh); make test runs it
	// from the checkout, where the files must be there.
	if os.Getenv("ANAS_ROOT_INTEGRATION") == "1" {
		t.Skip("needs the checkout's policy and packaging script; make test runs it")
	}
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	policyPath, serverPath := filepath.Join(repo, "ai", "assistant", "managed-config.yaml"), filepath.Join(repo, "ai", "assistant", "videocut-mcp.yaml")
	derive := func(policy, server string) (string, error) {
		out := filepath.Join(t.TempDir(), "config.yaml")
		command := exec.Command("bash", "-c", `set -euo pipefail; . "$1"; derive_plan_policy "$2" "$3" "$4"`, "derive",
			filepath.Join(repo, "scripts", "lib", "assistant-units.sh"), policy, server, out)
		if output, err := command.CombinedOutput(); err != nil {
			return "", &exec.Error{Name: string(output), Err: err}
		}
		return out, nil
	}
	read := func(path string) map[string]any {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err := yaml.Unmarshal(raw, &document); err != nil {
			t.Fatalf("%s is not the YAML it should be: %v", path, err)
		}
		return document
	}
	planPath, err := derive(policyPath, serverPath)
	if err != nil {
		t.Fatal(err)
	}
	policy, plan := read(policyPath), read(planPath)
	servers, _ := plan["mcp_servers"].(map[string]any)
	videocut, _ := servers["videocut"].(map[string]any)
	want := map[string]any{
		"command": "/usr/lib/a-nas/assistant/runtime/venv/bin/python", "args": []any{"/usr/lib/a-nas/assistant/anas_videocut_mcp.py"},
		// Full trust, as nobody is there to approve a call in a planning; and every key
		// written out, since the managed policy wins only for the keys it writes.
		"trust": "full", "enabled": true, "lazy": false, "timeout": 120,
		"tools": map[string]any{"prompts": false, "resources": false},
	}
	if !reflect.DeepEqual(videocut, want) {
		t.Fatalf("the videocut server in the planning policy = %#v", videocut)
	}
	skills, _ := plan["skills"].(map[string]any)
	if skills["creation_nudge_interval"] != 1 {
		t.Fatalf("the planning policy's skills = %#v", skills)
	}
	// Without them the two are the same: every other server, toolset and setting.
	delete(servers, "videocut")
	delete(skills, "creation_nudge_interval")
	if !reflect.DeepEqual(plan, policy) {
		t.Fatal("the planning policy differs from the assistant's policy in more than the videocut server and the reviews")
	}
	if base, _ := policy["mcp_servers"].(map[string]any); base["videocut"] != nil || len(base) == 0 {
		t.Fatalf("the assistant's own policy has servers %v", base)
	}
	// No platform's toolsets name it: only the planning instance gives it (HERMES_TUI_TOOLSETS).
	for platform, toolsets := range policy["platform_toolsets"].(map[string]any) {
		if slices.Contains(toolsets.([]any), any("videocut")) {
			t.Fatalf("platform %s lists the videocut toolset", platform)
		}
	}
	// A policy the server does not fit is refused when the package is built, and one that
	// was spliced into the wrong place, or twice, does not read as YAML.
	twice := filepath.Join(t.TempDir(), "twice.yaml")
	raw, _ := os.ReadFile(planPath)
	if err := os.WriteFile(twice, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := derive(twice, serverPath); err == nil {
		t.Fatalf("a policy that already has the videocut server was spliced again into %s", out)
	}
	server, _ := os.ReadFile(serverPath)
	doubled := strings.Replace(string(raw), "mcp_servers:\n", "mcp_servers:\n"+string(server), 1)
	var document map[string]any
	if err := yaml.Unmarshal([]byte(doubled), &document); err == nil {
		t.Fatal("a policy with the videocut server written twice read as YAML")
	}
	if _, err := memberConfig("alice", "hash", []byte(doubled)); err == nil {
		t.Fatal("the Host Agent wrote a member's settings from a policy with a key written twice")
	}
}
