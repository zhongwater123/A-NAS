package app

import (
	"os"
	"regexp"
	"testing"
)

// The planner (ai/assistant/videocut-planner.py) refuses a claim whose round
// id, job id or token is not what it expects, and so plans nothing. On the
// Experimental NAS the round script before it expected 24 letters where the
// VideoCut makes 26 (crypto/rand.Text): every round was claimed,
// thrown away and timed out, and no job was ever planned. Its patterns are
// read from the planner itself, so that the two cannot part.
func TestThePlannerAcceptsTheIDsAndTokensOfAClaim(t *testing.T) {
	script, err := os.ReadFile("../../../ai/assistant/videocut-planner.py")
	if err != nil {
		t.Fatal(err)
	}
	pattern := func(name string) *regexp.Regexp {
		t.Helper()
		found := regexp.MustCompile(`(?m)^` + name + ` = re\.compile\(r"([^"]+)"\)$`).FindSubmatch(script)
		if found == nil {
			t.Fatalf("videocut-planner.py has no %s pattern", name)
		}
		return regexp.MustCompile(`^(?:` + string(found[1]) + `)$`)
	}
	roundID, jobID, token := pattern("ROUND_ID"), pattern("JOB_ID"), pattern("TOKEN")
	for range 100 {
		if id := newID("round"); !roundID.MatchString(id) {
			t.Fatalf("the planner refuses round id %q", id)
		}
		if id := newID("job"); !jobID.MatchString(id) {
			t.Fatalf("the planner refuses job id %q", id)
		}
		if value := newToken(); !token.MatchString(value) {
			t.Fatalf("the planner refuses token %q", value)
		}
	}
}
