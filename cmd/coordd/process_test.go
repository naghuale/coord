//go:build unix

package main

import (
	"bufio"
	"context"
	"coord/checkpoint"
	"coord/envelope"
	"coord/fsm"
	"coord/internal/coorddconfig"
	"coord/store"
	"coord/transport"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const compositionHelperEnv = "COORDD_COMPOSITION_HELPER"

type journalEntry struct {
	Kind    envelope.Kind `json:"kind"`
	Run     string        `json:"run"`
	Attempt uint64        `json:"attempt"`
	Fence   uint64        `json:"fence"`
	Success bool          `json:"success"`
}

type processFakeTransport struct {
	session string
	in      chan envelope.Envelope
	journal string
	fail    bool
}

func (p *processFakeTransport) WhoAmI() string { return p.session }
func (p *processFakeTransport) Receive(context.Context) <-chan envelope.Envelope {
	return p.in
}
func (p *processFakeTransport) Send(_ context.Context, _ string, e envelope.Envelope) error {
	entry := journalEntry{Kind: e.Kind, Run: e.Run, Attempt: e.Attempt, Fence: e.Fence, Success: !p.fail}
	f, err := os.OpenFile(p.journal, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err = json.NewEncoder(f).Encode(entry); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if p.fail {
		return errors.New("test transport offline")
	}
	return nil
}

// TestCoorddCompositionHelper is the child entry point. It exercises the same
// config, lock, store, coordinator, daemon, and shutdown composition as coordd;
// only the external transport is replaced by a journaled fake.
func TestCoorddCompositionHelper(t *testing.T) {
	if os.Getenv(compositionHelperEnv) != "1" {
		return
	}
	configPath := os.Getenv("COORDD_CONFIG")
	journal := os.Getenv("COORDD_JOURNAL")
	failSend := os.Getenv("COORDD_SEND_FAIL") == "1"
	code := mainRunWithWriter([]string{"-config", configPath}, func(_ context.Context, _ coorddconfig.Config) (transport.Transport, func() error, error) {
		return &processFakeTransport{
			session: "coordd-process-test", in: make(chan envelope.Envelope), journal: journal, fail: failSend,
		}, func() error { return nil }, nil
	}, os.Stderr)
	os.Exit(code)
}

func TestCoorddProcessKillReleasesNamespaceAndRestarts(t *testing.T) {
	cfg, configPath := processConfig(t)
	seedProcessRun(t, cfg.StateDir, false)
	first := startCoorddProcess(t, configPath, filepath.Join(t.TempDir(), "first.jsonl"), true)
	waitProcessReady(t, first, cfg.ControlAddr)
	killCoorddProcess(t, first)

	second := startCoorddProcess(t, configPath, filepath.Join(t.TempDir(), "second.jsonl"), false)
	defer stopCoorddProcess(t, second)
	waitProcessReady(t, second, cfg.ControlAddr)
}

func TestCoorddSecondProcessCannotOpenOwnedNamespace(t *testing.T) {
	cfg, configPath := processConfig(t)
	first := startCoorddProcess(t, configPath, filepath.Join(t.TempDir(), "owner.jsonl"), false)
	defer stopCoorddProcess(t, first)
	waitProcessReady(t, first, cfg.ControlAddr)
	before := durableProcessFiles(t, cfg.StateDir)

	second := startCoorddProcess(t, configPath, filepath.Join(t.TempDir(), "second.jsonl"), false)
	err := second.wait()
	output := second.stderr.String()
	if err == nil || !strings.Contains(string(output), "namespace lock") {
		t.Fatalf("second coordd exit=%v output=%q; want namespace lock refusal", err, output)
	}
	if strings.Contains(string(output), "coordd: ready") {
		t.Fatalf("second coordd reported readiness: %q", output)
	}
	if after := durableProcessFiles(t, cfg.StateDir); !equalBytesMap(before, after) {
		t.Fatalf("second coordd changed durable state: before=%v after=%v", before, after)
	}
}

func TestCoorddProcessRestartRestoresAndReplaysOutboxBeforeReady(t *testing.T) {
	cfg, configPath := processConfig(t)
	seedProcessRun(t, cfg.StateDir, false)
	firstJournal := filepath.Join(t.TempDir(), "first.jsonl")
	first := startCoorddProcess(t, configPath, firstJournal, true)
	waitProcessReady(t, first, cfg.ControlAddr)
	killCoorddProcess(t, first)
	resetRetryForRestart(t, cfg.StateDir, "run-process")

	journal := filepath.Join(t.TempDir(), "replay.jsonl")
	second := startCoorddProcess(t, configPath, journal, false)
	defer stopCoorddProcess(t, second)
	waitProcessReady(t, second, cfg.ControlAddr)
	entries := readJournal(t, journal)
	if len(entries) != 1 || !entries[0].Success || entries[0].Kind != envelope.Task || entries[0].Run != "run-process" || entries[0].Attempt != 4 || entries[0].Fence != 19 {
		t.Fatalf("outbox not restored and delivered before readiness: %+v", entries)
	}
	stopCoorddProcess(t, second)
	run := loadProcessRun(t, cfg.StateDir, "run-process")
	if run.Attempt != 4 || run.Fence != 19 || run.Status != fsm.Pending {
		t.Fatalf("restored run changed identity/ownership: status=%s attempt=%d fence=%d", run.Status, run.Attempt, run.Fence)
	}
}

func TestCoorddProcessRestartPreservesResultAndReceipt(t *testing.T) {
	cfg, configPath := processConfig(t)
	seedProcessRun(t, cfg.StateDir, true)
	first := startCoorddProcess(t, configPath, filepath.Join(t.TempDir(), "first.jsonl"), true)
	waitProcessReady(t, first, cfg.ControlAddr)
	killCoorddProcess(t, first)
	resetRetryForRestart(t, cfg.StateDir, "run-process")

	journal := filepath.Join(t.TempDir(), "receipt.jsonl")
	second := startCoorddProcess(t, configPath, journal, false)
	defer stopCoorddProcess(t, second)
	waitProcessReady(t, second, cfg.ControlAddr)
	entries := readJournal(t, journal)
	if len(entries) != 1 || !entries[0].Success || entries[0].Kind != envelope.ResultAck || entries[0].Run != "run-process" || entries[0].Attempt != 4 || entries[0].Fence != 19 {
		t.Fatalf("result receipt not replayed before readiness: %+v", entries)
	}
	stopCoorddProcess(t, second)
	run := loadProcessRun(t, cfg.StateDir, "run-process")
	if run.Status != fsm.Done || run.Result == nil || run.Result.Outcome != "SUCCESS" || run.Attempt != 4 || run.Fence != 19 {
		t.Fatalf("committed result changed across restart: status=%s result=%+v attempt=%d fence=%d", run.Status, run.Result, run.Attempt, run.Fence)
	}
}

func TestCoorddProcessGracefulShutdownReleasesNamespace(t *testing.T) {
	cfg, configPath := processConfig(t)
	first := startCoorddProcess(t, configPath, filepath.Join(t.TempDir(), "first.jsonl"), false)
	waitProcessReady(t, first, cfg.ControlAddr)
	if err := first.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := first.wait(); err != nil {
		t.Fatalf("coordd graceful shutdown: %v (%s)", err, first.stderr.String())
	}
	second := startCoorddProcess(t, configPath, filepath.Join(t.TempDir(), "second.jsonl"), false)
	defer stopCoorddProcess(t, second)
	waitProcessReady(t, second, cfg.ControlAddr)
}

type coorddProcess struct {
	cmd       *exec.Cmd
	stderr    *strings.Builder
	waited    bool
	waitError error
	waitCh    chan error
}

func processConfig(t *testing.T) (coorddconfig.Config, string) {
	t.Helper()
	cfg := testConfig(t)
	cfg.ControlAddr, cfg.MetricsAddr = freeLoopbackAddrs(t)
	return cfg, writeTestConfig(t, cfg)
}

func startCoorddProcess(t *testing.T, configPath, journal string, fail bool) *coorddProcess {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCoorddCompositionHelper$")
	cmd.Env = append(os.Environ(), compositionHelperEnv+"=1", "COORDD_CONFIG="+configPath, "COORDD_JOURNAL="+journal)
	if fail {
		cmd.Env = append(cmd.Env, "COORDD_SEND_FAIL=1")
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &coorddProcess{cmd: cmd, stderr: &stderr, waitCh: make(chan error, 1)}
	go func() { p.waitCh <- cmd.Wait() }()
	return p
}

func waitProcessReady(t *testing.T, p *coorddProcess, address string) {
	t.Helper()
	client := &http.Client{Timeout: 250 * time.Millisecond}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		response, err := client.Get("http://" + address + "/readyz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		select {
		case waitErr := <-p.waitCh:
			p.waited, p.waitError = true, waitErr
			t.Fatalf("coordd exited before ready: %v (%s)", waitErr, p.stderr.String())
		case <-ticker.C:
		case <-timer.C:
			t.Fatalf("coordd did not become ready: last HTTP error=%v stderr=%q", err, p.stderr.String())
		}
	}
}

func (p *coorddProcess) wait() error {
	if p.waited {
		return p.waitError
	}
	p.waitError = <-p.waitCh
	p.waited = true
	return p.waitError
}

func killCoorddProcess(t *testing.T, p *coorddProcess) {
	t.Helper()
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := p.wait(); err == nil {
		t.Fatal("SIGKILLed coordd exited successfully")
	}
}

func stopCoorddProcess(t *testing.T, p *coorddProcess) {
	t.Helper()
	if p.waited {
		return
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case err := <-p.waitCh:
		p.waited, p.waitError = true, err
		if err != nil {
			t.Errorf("coordd shutdown: %v (%s)", err, p.stderr.String())
		}
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
		_ = p.wait()
		t.Error("coordd did not stop after SIGTERM")
	}
}

func seedProcessRun(t *testing.T, stateDir string, result bool) {
	t.Helper()
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.New(checkpoint.File{Path: filepath.Join(stateDir, "runs.json")})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	run := fsm.Run{
		Task: "task-process", RunID: "run-process", Attempt: 4, Fence: 19,
		TargetSHA: strings.Repeat("a", 40), OwnerSession: "executor-session",
		Status: fsm.Pending, AssignedAt: now, LastUpdate: now,
		NextUpdateBy: now.Add(time.Hour), Deadline: now.Add(24 * time.Hour), Worktree: stateDir,
		TaskMessages: []string{"task-message"},
		Outbox:       []envelope.Envelope{{SchemaVersion: 1, MessageID: "outbox-message", Timestamp: now, From: "coordd-process-test", To: "executor-session", Kind: envelope.Task, Task: "task-process", Run: "run-process", Attempt: 4, Fence: 19}},
	}
	if result {
		run.Status = fsm.Done
		run.Result = &envelope.ResultPayload{Outcome: "SUCCESS", Summary: "durable result"}
		run.Outbox[0].Kind = envelope.ResultAck
		run.Outbox[0].ResultAckPayload = &envelope.ResultAckPayload{Outcome: "SUCCESS"}
	}
	if err := snapshot.Update(func(runs map[string]fsm.Run) error { runs[run.RunID] = run; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
}

func resetRetryForRestart(t *testing.T, stateDir, runID string) {
	t.Helper()
	snapshot, err := store.New(checkpoint.File{Path: filepath.Join(stateDir, "runs.json")})
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Update(func(runs map[string]fsm.Run) error {
		run := runs[runID]
		run.SendRetryAt = time.Time{}
		runs[runID] = run
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
}

func loadProcessRun(t *testing.T, stateDir, runID string) fsm.Run {
	t.Helper()
	snapshot, err := store.New(checkpoint.File{Path: filepath.Join(stateDir, "runs.json")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := snapshot.Close(); err != nil {
			t.Errorf("close run snapshot: %v", err)
		}
	}()
	run, ok := snapshot.Get(runID)
	if !ok {
		t.Fatalf("run %s missing after restart", runID)
	}
	return run
}

func readJournal(t *testing.T, path string) []journalEntry {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Errorf("close transport journal: %v", err)
		}
	}()
	var entries []journalEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var entry journalEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return entries
}

func durableProcessFiles(t *testing.T, stateDir string) map[string][]byte {
	t.Helper()
	result := make(map[string][]byte)
	for _, name := range []string{"runs.json", "roles.json"} {
		path := filepath.Join(stateDir, name)
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		result[name] = data
	}
	return result
}

func equalBytesMap(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		other, ok := b[key]
		if !ok || string(value) != string(other) {
			return false
		}
	}
	return true
}

var _ transport.Transport = (*processFakeTransport)(nil)
