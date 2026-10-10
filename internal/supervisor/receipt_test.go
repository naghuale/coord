package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAtomicReceiptWriterWritesFile(t *testing.T) {
	root := t.TempDir()
	writer := AtomicReceiptWriter{ProjectRoot: root, Path: "evidence/receipt.json"}
	want := Receipt{
		SessionID:  "session-1",
		Generation: 2,
		ExitCode:   0,
		Events: []Event{{
			Type:   "process",
			Status: "complete",
			At:     time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC),
		}},
	}
	if err := writer.WriteReceipt(context.Background(), want); err != nil {
		t.Fatalf("WriteReceipt: %v", err)
	}

	path := filepath.Join(root, "evidence", "receipt.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read receipt: %v", err)
	}
	var got Receipt
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	if got.SessionID != want.SessionID || got.Generation != want.Generation || got.ExitCode != want.ExitCode {
		t.Fatalf("receipt mismatch: got %+v, want %+v", got, want)
	}
}

func TestAtomicReceiptWriterPathEscape(t *testing.T) {
	writer := AtomicReceiptWriter{ProjectRoot: t.TempDir(), Path: "../escape.json"}
	err := writer.WriteReceipt(context.Background(), Receipt{SessionID: "session-1"})
	if err == nil || !strings.Contains(err.Error(), "outside root") {
		t.Fatalf("expected outside root error, got %v", err)
	}
}
