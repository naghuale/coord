package checkpoint

import (
	"coord/fsm"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicSnapshotAndCorruption(t *testing.T) {
	f := File{Path: filepath.Join(t.TempDir(), "runs.json")}
	r := fsm.Run{RunID: "r", Attempt: 1, Fence: 1, OwnerSession: "exec"}
	if err := f.Save(map[string]fsm.Run{"r": r}); err != nil {
		t.Fatal(err)
	}
	got, err := f.Load()
	if err != nil || got["r"].Attempt != 1 {
		t.Fatal(err)
	}
	info, err := os.Stat(f.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("snapshot permissions")
	}
	if err = os.WriteFile(f.Path, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Load(); err == nil {
		t.Fatal("corrupt snapshot accepted")
	}
}
