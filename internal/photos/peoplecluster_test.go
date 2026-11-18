package photos

import (
	"slices"
	"testing"
)

// Clustering starts from the face with the most look-alikes, so that a
// person takes the faces that are surely theirs before a looser group does.
func TestDensestGroupIsTheOneWithTheMostNeighbours(t *testing.T) {
	pair, trio := []float32{1, 0, 0}, []float32{0, 1, 0}
	members := []clusterMember{{vector: pair}, {vector: pair}, {vector: trio}, {vector: trio}, {vector: trio}, {vector: []float32{0, 0, 1}}}

	got := densestGroup(members, []int{0, 1, 2, 3, 4, 5}, 0.9)
	slices.Sort(got)
	if !slices.Equal(got, []int{2, 3, 4}) {
		t.Fatalf("densestGroup() = %v, want the three alike [2 3 4], not the pair or the face alone", got)
	}
	// Only the faces still held count: with one of the three placed, the
	// first of the two pairs wins.
	got = densestGroup(members, []int{0, 1, 3, 4, 5}, 0.9)
	slices.Sort(got)
	if !slices.Equal(got, []int{0, 1}) {
		t.Fatalf("densestGroup() among the held = %v, want [0 1]", got)
	}
	if got := densestGroup(members, nil, 0.9); len(got) != 0 {
		t.Fatalf("densestGroup() of nothing = %v", got)
	}
}
