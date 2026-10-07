package checkpoint

import (
	"coord/fsm"
	"path/filepath"
	"testing"
)

func TestSaveRejectsStateThatCannotBeReloaded(t *testing.T) {
	f := File{Path: filepath.Join(t.TempDir(), "runs.json")}
	good := fsm.Run{RunID: "r", Attempt: 1, Fence: 1, OwnerSession: "exec"}
	if err := f.Save(map[string]fsm.Run{"r": good}); err != nil {
		t.Fatal(err)
	}
	bad := good
	bad.Fence = 0
	if err := f.Save(map[string]fsm.Run{"r": bad}); err == nil {
		t.Fatal("unreadable snapshot written")
	}
	runs, err := f.Load()
	if err != nil {
		t.Fatal(err)
	}
	if runs["r"].Fence != 1 {
		t.Fatal("failed save replaced good snapshot")
	}
}
