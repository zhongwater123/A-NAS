package capacity

import "testing"

func TestAdmitsKeepsTheReserveFree(t *testing.T) {
	const gib = uint64(1 << 30)
	for _, test := range []struct {
		name                       string
		total, available, incoming uint64
		want                       bool
	}{
		{"5% of a large volume", 1000 * gib, 60 * gib, 10 * gib, true},
		{"exactly the 5% reserve left", 1000 * gib, 60 * gib, 10*gib + 1, false},
		{"small volume keeps 10 GiB", 100 * gib, 15 * gib, 5 * gib, true},
		{"small volume below 10 GiB", 100 * gib, 15 * gib, 5*gib + 1, false},
		{"more than is available", 100 * gib, 15 * gib, 16 * gib, false},
		{"nothing incoming on a full volume", 100 * gib, 9 * gib, 0, false},
	} {
		if got := admits(test.total, test.available, test.incoming); got != test.want {
			t.Errorf("%s: admits(%d, %d, %d) = %v, want %v", test.name, test.total, test.available, test.incoming, got, test.want)
		}
	}
}
