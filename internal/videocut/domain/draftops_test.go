package domain

import "testing"

// A beat is written "card/n" and read back as it was written, wherever it
// is made or taken apart.
func TestABeatIsReadAsItIsWritten(t *testing.T) {
	for _, c := range []struct {
		card string
		n    int
	}{{"v1-3", 1}, {"c2", 12}} {
		id := BeatID(c.card, c.n)
		if card, n, ok := SplitBeat(id); !ok || card != c.card || n != c.n || CardOf(id) != c.card {
			t.Errorf("%s read as %q %d %v, card %q", id, card, n, ok, CardOf(id))
		}
	}
	if BeatID("v1-3", 1) != "v1-3/1" {
		t.Errorf("beatID() = %q", BeatID("v1-3", 1))
	}
	for _, text := range []string{"", "c1", "c1/", "c1/0", "c1/-1", "c1/01", "c1/+1", "c1/x", "c1/1 "} {
		if card, n, ok := SplitBeat(text); ok {
			t.Errorf("%q read as beat %d of %q", text, n, card)
		}
	}
}
