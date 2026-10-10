package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type AtomicReceiptWriter struct{ ProjectRoot, Path string }

func (w AtomicReceiptWriter) WriteReceipt(_ context.Context, r Receipt) error {
	root, _ := filepath.Abs(w.ProjectRoot)
	target, _ := filepath.Abs(filepath.Join(root, w.Path))
	rel, _ := filepath.Rel(root, target)
	if rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return errors.New("outside root")
	}
	tmp := filepath.Join(root, ".scratch", "tmp", "receipts")
	if e := os.MkdirAll(tmp, 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(tmp, "receipt-*.json")
	if e != nil {
		return e
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if e = enc.Encode(r); e == nil {
		e = f.Sync()
	}
	if x := f.Close(); e == nil {
		e = x
	}
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(target), 0700); e != nil {
		return e
	}
	return os.Rename(name, target)
}
