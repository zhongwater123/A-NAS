package linux

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Output is what parsed commands use: a warning on standard error must not
// reach the parser, and must still explain a failure.
func TestOutputKeepsStandardErrorForFailures(t *testing.T) {
	ctx := context.Background()
	runner := execCommandRunner{}
	warns := []string{"-c", `echo "Warning on standard error" >&2; echo '{"sessions": {}}'`}

	output, err := runner.Output(ctx, "sh", warns, "")
	if err != nil || string(output) != "{\"sessions\": {}}\n" {
		t.Fatalf("Output() = %q, %v; want standard output alone", output, err)
	}
	if combined, err := runner.Run(ctx, "sh", warns, ""); err != nil || !strings.Contains(string(combined), "Warning") {
		t.Fatalf("Run() = %q, %v; want both streams", combined, err)
	}

	_, err = runner.Output(ctx, "sh", []string{"-c", "echo 'no such account' >&2; exit 2"}, "")
	var coded exitCoder
	if !errors.As(err, &coded) || coded.ExitCode() != 2 || !strings.Contains(err.Error(), "no such account") {
		t.Fatalf("Output() error = %v; want exit status 2 with standard error", err)
	}
}
