package montage

import (
	"os"
	"regexp"
	"testing"
)

// The round script (ai/assistant/montage_round.py) refuses a claim whose
// round id, job id or token is not what it expects, and so plans nothing.
// On the Experimental NAS it expected 24 letters where the montage center
// makes 26 (crypto/rand.Text): every round was claimed, thrown away and
// timed out, and no job was ever planned. Its patterns are read from the
// script itself, so that the two cannot part again.
func TestTheRoundScriptAcceptsTheIDsAndTokensOfAClaim(t *testing.T) {
	script, err := os.ReadFile("../../ai/assistant/montage_round.py")
	if err != nil {
		t.Fatal(err)
	}
	pattern := func(name string) *regexp.Regexp {
		t.Helper()
		found := regexp.MustCompile(`(?m)^` + name + ` = re\.compile\(r"([^"]+)"\)$`).FindSubmatch(script)
		if found == nil {
			t.Fatalf("montage_round.py has no %s pattern", name)
		}
		return regexp.MustCompile(`^(?:` + string(found[1]) + `)$`)
	}
	roundID, jobID, token := pattern("ROUND_ID"), pattern("JOB_ID"), pattern("TOKEN")
	for range 100 {
		if id := newID("round"); !roundID.MatchString(id) {
			t.Fatalf("the round script refuses round id %q", id)
		}
		if id := newID("job"); !jobID.MatchString(id) {
			t.Fatalf("the round script refuses job id %q", id)
		}
		if value := newToken(); !token.MatchString(value) {
			t.Fatalf("the round script refuses token %q", value)
		}
	}
}
