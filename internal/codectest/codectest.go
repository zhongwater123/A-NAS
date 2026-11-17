// Package codectest says what a test of the real codecs does on a machine
// without them. FFmpeg and libheif's tools are not on every development
// machine, nor on the CI runner's own system, so such a test is skipped
// there. Where they are installed for it, a skip would hide that the test no
// longer runs (go test prints no skips without -v): with
// ANAS_CODECS_REQUIRED=1 a missing codec fails the test instead, as
// ANAS_QDRANT_REQUIRED does for the vector index. scripts/codec-test.sh sets
// it in the Debian 13 image that has the codecs the device has (#425).
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
