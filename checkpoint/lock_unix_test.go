//go:build unix

package checkpoint

import (
	"path/filepath"
	"testing"
)

func TestExclusiveOwnership(t *testing.T) {
	f := File{Path: filepath.Join(t.TempDir(), "runs.json")}
	release, err := f.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if unlock, err := f.Lock(); err == nil {
		unlock()
		t.Fatal("second coordinator acquired snapshot")
	}
	if err = release(); err != nil {
		t.Fatal(err)
	}
	unlock, err := f.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
}
