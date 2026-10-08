//go:build unix

package main

import (
	"bufio"
	"context"
	"coord/checkpoint"
	"coord/envelope"
	"coord/fsm"
	"coord/internal/coorddconfig"
	"coord/internal/filelock"
	"coord/store"
	"coord/task"
	"coord/taskid"
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
	if got := invokeDaemonHelper(t, "try_store", cfg.StateDir); got != "REFUSED\n" {
		t.Fatalf("legacy writer opened runs.json while coordd owned compatibility lock: %q", got)
	}
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

func TestCoorddLegacyWriterLockKeepsDaemonNotReady(t *testing.T) {
	cfg, configPath := processConfig(t)
	legacyPath := filepath.Join(cfg.StateDir, "runs.json")
	legacyWriter, _ := startDaemonHelper(t, "lock", legacyPath+".lock")
	defer killDaemonHelper(t, legacyWriter)
	if release, err := filelock.Lock(legacyPath + ".lock"); err == nil {
		_ = release()
		t.Fatal("legacy helper did not retain the compatibility lock")
	} else if !errors.Is(err, filelock.ErrLocked) {
		t.Fatalf("unexpected legacy lock check error: %v", err)
	}
	journal := filepath.Join(t.TempDir(), "must-not-exist.jsonl")
	proc := startCoorddProcess(t, configPath, journal, false)
	defer stopCoorddProcess(t, proc)
	waitProcessStatus(t, proc, cfg.ControlAddr, http.StatusServiceUnavailable)
	if _, err := os.Stat(journal); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transport ran while legacy writer held ownership: stat err=%v", err)
	}
	if got := durableProcessFiles(t, cfg.StateDir); len(got) != 0 {
		t.Fatalf("diagnostic-only daemon changed durable state: %v", got)
	}
	stopCoorddProcess(t, proc)
	if !strings.Contains(proc.stderr.String(), "legacy writer owns namespace") {
		t.Fatalf("daemon did not report the held compatibility lock: %q", proc.stderr.String())
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

func TestCoorddRestartRestoresCanonicalTaskStateAndOutbox(t *testing.T) {
	cfg, configPath := processConfig(t)
	eventID := seedCanonicalTaskRestartState(t, cfg.StateDir)

	firstJournal := filepath.Join(t.TempDir(), "first.jsonl")
	first := startCoorddProcess(t, configPath, firstJournal, true)
	waitProcessReady(t, first, cfg.ControlAddr)
	firstEntries := readJournal(t, firstJournal)
	if len(firstEntries) != 1 || firstEntries[0].Success || firstEntries[0].Kind != envelope.ResultAck {
		t.Fatalf("first startup send = %+v; want one failed ResultAck delivery", firstEntries)
	}
	killCoorddProcess(t, first)

	canonicalFile := checkpoint.CanonicalFile{Path: filepath.Join(cfg.StateDir, "canonical.json")}
	afterFailure, err := canonicalFile.Load()
	if err != nil {
		t.Fatal(err)
	}
	queued := afterFailure.Outbox["run-task-process"].Messages
	if len(queued) != 1 || queued[0].EventID != eventID {
		t.Fatalf("failed first send did not retain seeded EventID %q: %+v", eventID, queued)
	}
	resetRetryForRestart(t, cfg.StateDir, "run-task-process")

	replayJournal := filepath.Join(t.TempDir(), "replay.jsonl")
	second := startCoorddProcess(t, configPath, replayJournal, false)
	waitProcessReady(t, second, cfg.ControlAddr)
	secondEntries := readJournal(t, replayJournal)
	if len(secondEntries) != 1 || !secondEntries[0].Success || secondEntries[0].Kind != envelope.ResultAck ||
		secondEntries[0].Run != "run-task-process" || secondEntries[0].Attempt != 1 || secondEntries[0].Fence != 1 {
		t.Fatalf("restored Outbox not replayed before readiness: %+v", secondEntries)
	}
	stopCoorddProcess(t, second)

	restored, err := canonicalFile.Load()
	if err != nil {
		t.Fatal(err)
	}
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if restored.Tasks[id].Revision != 2 || restored.Tasks[id].Fence != 1 || restored.Tasks[id].LastEventSeq != 2 {
		t.Fatalf("Task state changed across Outbox replay: %+v", restored.Tasks[id])
	}
	run := restored.Runs["run-task-process"]
	if run.TaskID != id || run.Status != fsm.Done || run.Attempt != 1 || run.Fence != 1 {
		t.Fatalf("Task-scoped Run relation/state changed across restart: %+v", run)
	}
	if len(restored.Events) != 2 || restored.Events[1].ID != eventID || len(restored.Inbox) != 1 {
		t.Fatalf("transport delivery changed Task Events or Inbox acceptance: events=%+v inbox=%+v", restored.Events, restored.Inbox)
	}
	if _, exists := restored.Outbox[run.RunID]; exists {
		t.Fatalf("successfully delivered Outbox was not removed: %+v", restored.Outbox[run.RunID])
	}
}

func seedCanonicalTaskRestartState(t *testing.T, stateDir string) string {
	t.Helper()
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	now := time.Now().UTC().Truncate(time.Second)
	state := store.EmptyCanonicalState()
	state.Tasks[id] = task.Task{ID: id, Title: "restart persistence", OwnerID: "owner", Revision: 2, LastEventSeq: 2, Fence: 1,
		StagePlan: task.StagePlan{Stages: []task.Stage{task.Execution}, PolicyVersion: task.StagePlanPolicyVersion},
		Status:    task.Completed, CreatedAt: now, UpdatedAt: now.Add(time.Second)}
	run := fsm.Run{Task: "legacy task label", TaskID: id, RunID: "run-task-process", Attempt: 1, Fence: 1,
		TargetSHA: strings.Repeat("a", 40), OwnerSession: "executor-session", Worktree: stateDir, Deadline: now.Add(time.Hour), Status: fsm.Done,
		Result: &envelope.ResultPayload{Outcome: "SUCCESS", Summary: "durable result"}, ResultMessageID: "result-message"}
	state.Runs[run.RunID] = run
	created := task.DomainEvent{ID: task.EventID(id, 1), TaskID: id, EventSeq: 1, TaskRevision: 1,
		Type: task.TaskCreated, ActorID: "owner", CreatedAt: now}
	transferred := task.DomainEvent{ID: task.EventID(id, 2), TaskID: id, RunID: run.RunID, EventSeq: 2, TaskRevision: 2,
		TaskFence: 1, Type: task.TaskOwnershipTransferred, CreatedAt: now.Add(time.Second)}
	state.Events = []task.DomainEvent{created, transferred}
	env := envelope.Envelope{SchemaVersion: 1, MessageID: "task-outbox-message", Timestamp: now.Add(2 * time.Second),
		From: "coordd-process-test", To: run.OwnerSession, Kind: envelope.ResultAck, Task: run.Task, Run: run.RunID,
		Attempt: run.Attempt, Fence: run.Fence, TargetSHA: strings.Repeat("a", 40), InReplyTo: "result-message",
		ResultAckPayload: &envelope.ResultAckPayload{Outcome: "SUCCESS"}}
	state.Outbox[run.RunID] = store.OutboxQueue{Messages: []store.OutboxMessage{{Envelope: env, EventID: transferred.ID}}}
	receipt := store.InboxReceipt{ActorID: "owner", MessageID: "owner-command", TaskID: id, RunID: run.RunID,
		EventIDs: []string{transferred.ID}, ExpectedRevision: 1, CommittedRevision: 2, RecordedAt: now.Add(time.Second)}
	state.Inbox[store.InboxKey(receipt.ActorID, receipt.MessageID)] = receipt
	if err := (checkpoint.CanonicalFile{Path: filepath.Join(stateDir, "canonical.json")}).Save(state); err != nil {
		t.Fatal(err)
	}
	return transferred.ID
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
	waitProcessStatus(t, p, address, http.StatusOK)
}

func waitProcessStatus(t *testing.T, p *coorddProcess, address string, want int) {
	t.Helper()
	client := &http.Client{Timeout: 250 * time.Millisecond}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	lastStatus := 0
	for {
		response, err := client.Get("http://" + address + "/readyz")
		if err == nil {
			lastStatus = response.StatusCode
			_ = response.Body.Close()
			if response.StatusCode == want {
				return
			}
		}
		select {
		case waitErr := <-p.waitCh:
			p.waited, p.waitError = true, waitErr
			t.Fatalf("coordd exited before HTTP status %d: %v (%s)", want, waitErr, p.stderr.String())
		case <-ticker.C:
		case <-timer.C:
			t.Fatalf("coordd did not return HTTP status %d: last status=%d HTTP error=%v stderr=%q", want, lastStatus, err, p.stderr.String())
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
	// The daemon's configured graceful-shutdown deadline is 10 seconds; leave
	// room for process scheduling under the race-enabled full suite.
	case <-time.After(12 * time.Second):
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
	snapshot, err := store.NewCanonical(checkpoint.CanonicalFile{Path: filepath.Join(stateDir, "canonical.json")})
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
		Outbox: []envelope.Envelope{{
			SchemaVersion: 1, MessageID: "outbox-message", Timestamp: now,
			From: "coordd-process-test", To: "executor-session", Kind: envelope.Task,
			Task: "task-process", Run: "run-process", Attempt: 4, Fence: 19,
			TargetSHA:   strings.Repeat("a", 40),
			TaskPayload: &envelope.TaskPayload{Worktree: stateDir, Deadline: now.Add(24 * time.Hour)},
		}},
	}
	if result {
		run.Status = fsm.Done
		run.Result = &envelope.ResultPayload{Outcome: "SUCCESS", Summary: "durable result"}
		run.Outbox[0].Kind = envelope.ResultAck
		run.Outbox[0].TaskPayload = nil
		run.Outbox[0].InReplyTo = "result-message"
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
	snapshot, err := store.NewCanonical(checkpoint.CanonicalFile{Path: filepath.Join(stateDir, "canonical.json")})
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
	snapshot, err := store.NewCanonical(checkpoint.CanonicalFile{Path: filepath.Join(stateDir, "canonical.json")})
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
	for _, name := range []string{"runs.json", "canonical.json", "roles.json"} {
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
