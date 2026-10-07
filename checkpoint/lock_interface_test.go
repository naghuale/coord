package checkpoint

import (
	"path/filepath"
	"testing"
)

// TestLockOwnsSnapshotOrRefusesWithoutRelease pins the fail-closed contract on
// every platform: Lock either takes ownership or reports that it cannot, and a
// refusal never hands back a usable release function.
func TestLockOwnsSnapshotOrRefusesWithoutRelease(t *testing.T) {
	release, err := File{Path: filepath.Join(t.TempDir(), "runs.json")}.Lock()
	if err != nil {
		if release != nil {
			t.Fatal("refused lock returned a release function")
		}
		return
	}
	if err = release(); err != nil {
		t.Fatal(err)
	}
}
