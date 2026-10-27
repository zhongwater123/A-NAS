package linux

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Survey counts only the subvolumes named the way snapshotRoot names them,
// so a stray file or folder beside them is not taken for a snapshot.
func TestSurveyCountsSnapshotSubvolumesOnly(t *testing.T) {
	executor := NewExecutor(nil, nil, Options{SystemRoot: t.TempDir(), MountPoint: t.TempDir()})
	survey, err := executor.surveySnapshots([]string{"snapshot:kept"})
	if err != nil || len(survey.Present) != 0 || survey.Unknown != 0 {
		t.Fatalf("surveySnapshots() before any snapshot = %+v, %v", survey, err)
	}
	for _, folder := range []string{
		executor.snapshotRoot("snapshot:kept"),
		executor.snapshotRoot("snapshot:of-a-lost-catalog"),
		filepath.Join(executor.snapshotsRoot(), "notes"),
	} {
		if err := os.MkdirAll(folder, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(executor.snapshotsRoot(), strings.Repeat("a", 32)), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	survey, err = executor.surveySnapshots([]string{"snapshot:kept", "snapshot:gone"})
	if err != nil || !slices.Equal(survey.Present, []string{"snapshot:kept"}) || survey.Unknown != 1 {
		t.Fatalf("surveySnapshots() = %+v, %v; want snapshot:kept present and 1 unknown", survey, err)
	}
}
