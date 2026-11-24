package linux

import "testing"

func TestRunsAsMatchesAnyUserIDOfTheUidLine(t *testing.T) {
	status := "Name:\tsleep\nUmask:\t0022\nUid:\t0\t20171\t0\t0\nGid:\t0\t0\t0\t0\n"
	for uid, want := range map[string]bool{"20171": true, "0": true, "2017": false, "20172": false} {
		if got := runsAs(status, uid); got != want {
			t.Errorf("runsAs(%s) = %v, want %v", uid, got, want)
		}
	}
	if runsAs("Name:\tkworker\n", "0") {
		t.Error("a status without a Uid line matched")
	}
}
