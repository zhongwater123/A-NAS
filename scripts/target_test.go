//go:build linux

package scripts_test

import (
	"crypto/sha256"
	"debug/elf"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func repository(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(dir)
}

func environment(extra ...string) []string {
	keys := map[string]bool{}
	for _, entry := range extra {
		keys[strings.SplitN(entry, "=", 2)[0]] = true
	}
	var result []string
	for _, entry := range os.Environ() {
		if !keys[strings.SplitN(entry, "=", 2)[0]] {
			result = append(result, entry)
		}
	}
	return append(result, extra...)
}

func shell(t *testing.T, arch, body string, args ...string) ([]byte, error) {
	t.Helper()
	parameters := append([]string{"-euc", body, "target-test", repository(t)}, args...)
	command := exec.Command("bash", parameters...)
	command.Env = environment("ANAS_TARGET_ARCH=" + arch)
	return command.CombinedOutput()
}

func put(t *testing.T, root, name, text string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTargetInputsFailClosed(t *testing.T) {
	root := t.TempDir()
	put(t, root, "deploy/qdrant/qdrant.json", "amd64")
	body := `. "$1/scripts/lib/target.sh"; target_file "$2" deploy/qdrant/qdrant.json`
	for _, arch := range []string{"arm64", "unsupported"} {
		if output, err := shell(t, arch, body, root); err == nil {
			t.Fatalf("%s accepted missing/unsupported input: %s", arch, output)
		}
	}
	put(t, root, "deploy/qdrant/qdrant-arm64.json", "arm64")
	output, err := shell(t, "arm64", body, root)
	if err != nil || strings.TrimSpace(string(output)) != filepath.Join(root, "deploy/qdrant/qdrant-arm64.json") {
		t.Fatalf("arm64 target: %s, %v", output, err)
	}
	// Resolving a lock must also stop before uv can overwrite a native lock.
	lock := `. "$1/scripts/lib/python-runtime.sh"; uv() { return 0; }; lock_runtime input output`
	if output, err := shell(t, "unsupported", lock); err == nil {
		t.Fatalf("unsupported lock target was accepted: %s", output)
	}
}

func TestRuntimeIdentitySeparatesIdenticalPlatformLocks(t *testing.T) {
	root := t.TempDir()
	for _, file := range []string{"ai/requirements.txt", "ai/requirements-arm64.txt", "deploy/search/requirements.txt", "deploy/search/requirements-arm64.txt"} {
		put(t, root, file, "example==1\n")
	}
	put(t, root, "deploy/search/searxng.json", `{"sha256":"`+strings.Repeat("a", 64)+`"}`)
	for _, kind := range []string{"ai", "search"} {
		body := `. "$1/scripts/lib/content.sh"; ` + kind + `_runtime_id "$2"`
		amd64, err := shell(t, "amd64", body, root)
		if err != nil {
			t.Fatalf("amd64: %s, %v", amd64, err)
		}
		arm64, err := shell(t, "arm64", body, root)
		if err != nil {
			t.Fatalf("arm64: %s, %v", arm64, err)
		}
		if string(amd64) == string(arm64) {
			t.Fatalf("%s runtime identities collide", kind)
		}
		legacy := "example==1\npython=3.13\n"
		if kind == "search" {
			legacy += "searxng=" + strings.Repeat("a", 64) + "\n"
		}
		want := fmt.Sprintf("%x", sha256.Sum256([]byte(legacy)))[:16]
		if strings.TrimSpace(string(amd64)) != want {
			t.Fatalf("changed legacy %s identity: got %s want %s", kind, amd64, want)
		}
	}
}

func TestELFValidationRejectsArchitectureRelabeling(t *testing.T) {
	root := t.TempDir()
	put(t, root, "main.go", "package main\nfunc main() {}\n")
	for _, arch := range []string{"amd64", "arm64"} {
		binary := filepath.Join(root, "hello-"+arch)
		command := exec.Command("go", "build", "-o", binary, filepath.Join(root, "main.go"))
		command.Env = environment("GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %s, %v", arch, output, err)
		}
		file, err := elf.Open(binary)
		if err != nil {
			t.Fatal(err)
		}
		want := elf.EM_X86_64
		if arch == "arm64" {
			want = elf.EM_AARCH64
		}
		if file.Machine != want {
			t.Fatalf("fixture is %v, want %v", file.Machine, want)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		body := `. "$1/scripts/lib/target.sh"; require_target_elf "$2"`
		if output, err := shell(t, arch, body, binary); err != nil {
			t.Fatalf("correct %s: %s, %v", arch, output, err)
		}
		other := "amd64"
		if arch == "amd64" {
			other = "arm64"
		}
		if output, err := shell(t, other, body, binary); err == nil {
			t.Fatalf("relabeled %s as %s: %s", arch, other, output)
		}
	}
}

func TestDebValidationRejectsArchitectureRelabeling(t *testing.T) {
	root := t.TempDir()
	for _, arch := range []string{"amd64", "arm64", "all"} {
		packageRoot := filepath.Join(root, arch)
		put(t, packageRoot, "DEBIAN/control", "Package: target-fixture\nVersion: 1\nArchitecture: "+arch+"\nMaintainer: Test <test@example.invalid>\nDescription: target architecture fixture\n")
		deb := filepath.Join(root, arch+".deb")
		command := exec.Command("dpkg-deb", "--root-owner-group", "--build", packageRoot, deb)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %s, %v", arch, output, err)
		}
		for _, target := range []string{"amd64", "arm64"} {
			body := `. "$1/scripts/lib/target.sh"; require_target_deb "$2"`
			output, err := shell(t, target, body, deb)
			if wantSuccess := arch == target || arch == "all"; (err == nil) != wantSuccess {
				t.Fatalf("%s package for %s: %s, %v", arch, target, output, err)
			}
		}
	}
}
