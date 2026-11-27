// Package codectest handles missing FFmpeg and libheif tools or codecs in tests.
// scripts/codec-test.sh requires them in the device's Debian 13 image (#425).
package codectest

import (
	"os"
	"testing"
)

// Required is the environment variable that turns a missing codec from a
// skip into a failure.
const Required = "ANAS_CODECS_REQUIRED"

// Missing ends a test that needs a codec this machine lacks, or one the
// installed tools cannot produce a sample with: skipped, or failed when the
// codecs are required.
func Missing(t testing.TB, format string, args ...any) {
	t.Helper()
	if os.Getenv(Required) == "1" {
		t.Fatalf("the codecs are required ("+Required+"=1): "+format, args...)
	}
	t.Skipf(format, args...)
}
