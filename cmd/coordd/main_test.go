//go:build unix

package main

import (
	"bufio"
	"bytes"
	"context"
	"coord"
	"coord/checkpoint"
	"coord/envelope"
	"coord/fsm"
	"coord/internal/coorddconfig"
	"coord/internal/filelock"
	"coord/roles"
	"coord/store"
	"coord/task"
	"coord/taskid"
	"coord/transport"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type idleTransport struct {
	session string
	in      chan envelope.Envelope
	sendErr error
	sent    int
}

// Keep helper-owned storage roots alive for the lifetime of the subprocess.
// Otherwise GC can close the flock file descriptor and make the lock test flaky.
var processHelperStore *store.Store
var processHelperCoordinator *coord.Coordinator

func (t *idleTransport) WhoAmI() string { return t.session }
func (t *idleTransport) Send(context.Context, string, envelope.Envelope) error {
	t.sent++
	return t.sendErr
}
func (t *idleTransport) Receive(context.Context) <-chan envelope.Envelope { return t.in }

func testConfig(t *testing.T) coorddconfig.Config {
	t.Helper()
	dir := t.TempDir()
	server := filepath.Join(dir, "server.mjs")
	if err := os.WriteFile(server, []byte("placeholder"), 0600); err != nil {
		t.Fatal(err)
	}
	home, presence := filepath.Join(dir, "telepathy-home"), filepath.Join(dir, "presence")
	for _, path := range []string{home, presence} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peer, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tokenPath, keyPath := filepath.Join(dir, "control.token"), filepath.Join(dir, "private.key")
	if err := os.WriteFile(tokenPath, []byte("token"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(private)), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := coorddconfig.Config{
		SchemaVersion: 1, StateDir: filepath.Join(dir, "state"),
		ControlAddr: "127.0.0.1:0", MetricsAddr: "127.0.0.1:0", ControlTokenFile: tokenPath,
		Transport: coorddconfig.Transport{
			Kind: "telepathy", Server: server, Home: home, Directory: presence,
			AnchorPID: 123, Session: "opencode-123", PrivateKeyFile: keyPath,
			Peers: map[string]string{"opencode-456": base64.StdEncoding.EncodeToString(peer)},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestCoorddRefusesStartWithoutTransportBeforeLock(t *testing.T) {
	cfg := testConfig(t)
	cfg.Transport = coorddconfig.Transport{}
	called := false
	path := writeTestConfig(t, cfg)
	var stderr strings.Builder
	code := mainRunWithWriter([]string{"-config", path}, func(context.Context, coorddconfig.Config) (transport.Transport, func() error, error) {
		called = true
		return nil, nil, errors.New("factory should not run")
	}, &stderr)
	if code != 2 || called {
		t.Fatalf("missing transport exit=%d factory-called=%v stderr=%q", code, called, stderr.String())
	}
	if !strings.Contains(stderr.String(), "ownership: not acquired") || !strings.Contains(stderr.String(), "namespace lock: not acquired") {
		t.Fatalf("missing fail-closed ownership explanation: %q", stderr.String())
	}
	if _, statErr := os.Stat(cfg.StateDir); !os.IsNotExist(statErr) {
		t.Fatalf("state namespace was touched before transport validation: %v", statErr)
	}
	// A separate OS process can take the exact namespace lock immediately after
	// this rejected startup; configuration validation created no ownership state.
	child, _ := startDaemonHelper(t, "lock", filepath.Join(cfg.StateDir, "namespace.lock"))
	killDaemonHelper(t, child)
}

func TestCoorddRejectsUnknownTransportKind(t *testing.T) {
	cfg := testConfig(t)
	cfg.Transport.Kind = "mock"
	path := writeTestConfig(t, cfg)
	var stderr strings.Builder
	code := mainRunWithWriter([]string{"-config", path}, nil, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "supported transports: telepathy") || !strings.Contains(stderr.String(), "namespace lock: not acquired") {
		t.Fatalf("unknown transport not rejected before ownership: code=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(cfg.StateDir); !os.IsNotExist(err) {
		t.Fatalf("unknown transport touched state namespace: %v", err)
	}
}

func TestCoorddInvalidConfigExitsTwoBeforeSideEffects(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":2,"state_dir":"`+stateDir+`","unexpected":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	if code := mainRunWithWriter([]string{"-config", path}, nil, &stderr); code != 2 {
		t.Fatalf("invalid config exit code = %d, want 2", code)
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("invalid config caused state side effect: %v", err)
	}
	if code := mainRunWithWriter([]string{"-config", path, "-control-addr", "127.0.0.1:9000"}, nil, &stderr); code != 2 {
		t.Fatalf("override flag exit code = %d, want 2", code)
	}
}

func TestCoorddTakesNamespaceLockBeforeStores(t *testing.T) {
	cfg := testConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	factoryErr := errors.New("stop after ownership assertion")
	assertHeld := func(path string) {
		t.Helper()
		release, lockErr := filelock.Lock(path)
		if lockErr == nil {
			_ = release()
			t.Errorf("ownership was not held before opening the next resource: %s", path)
		} else if !errors.Is(lockErr, filelock.ErrLocked) {
			t.Errorf("unexpected lock error for %s: %v", path, lockErr)
		}
	}
	err := runWithFactories(ctx, cfg,
		func(_ context.Context, _ coorddconfig.Config) (transport.Transport, func() error, error) {
			for _, path := range []string{
				filepath.Join(cfg.StateDir, "namespace.lock"),
				filepath.Join(cfg.StateDir, "runs.json.lock"),
				filepath.Join(cfg.StateDir, "canonical.json.lock"),
				filepath.Join(cfg.StateDir, "roles.json.lock"),
			} {
				assertHeld(path)
			}
			return nil, nil, factoryErr
		},
		func(path string) (store.RunStore, error) {
			assertHeld(filepath.Join(cfg.StateDir, "namespace.lock"))
			assertHeld(filepath.Join(cfg.StateDir, "runs.json.lock"))
			return store.NewCanonical(checkpoint.CanonicalFile{Path: path})
		},
		func(path string) (*roles.Registry, error) {
			assertHeld(filepath.Join(cfg.StateDir, "namespace.lock"))
			return roles.New(path)
		},
	)
	if !errors.Is(err, factoryErr) {
		t.Fatalf("run error = %v, want factory error", err)
	}
	for _, path := range []string{filepath.Join(cfg.StateDir, "namespace.lock"), filepath.Join(cfg.StateDir, "runs.json.lock"), filepath.Join(cfg.StateDir, "canonical.json.lock"), filepath.Join(cfg.StateDir, "roles.json.lock")} {
		release, lockErr := filelock.Lock(path)
		if lockErr != nil {
			t.Fatalf("lock remains after failed transport startup (%s): %v", path, lockErr)
		}
		_ = release()
	}
}

func TestCoorddActiveLegacyRunKeepsNotReadyWithoutStartingTransport(t *testing.T) {
	cfg := testConfig(t)
	cfg.ControlAddr, cfg.MetricsAddr = freeLoopbackAddrs(t)
	legacyPath := filepath.Join(cfg.StateDir, "runs.json")
	now := time.Now().UTC()
	active := fsm.Run{RunID: "legacy-active", Attempt: 1, Fence: 1, OwnerSession: "executor", Status: fsm.Pending, AssignedAt: now, LastUpdate: now}
	if err := (checkpoint.File{Path: legacyPath}).Save(map[string]fsm.Run{active.RunID: active}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stderr strings.Builder
	factoryCalled := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- runWithWriter(ctx, cfg, func(context.Context, coorddconfig.Config) (transport.Transport, func() error, error) {
			factoryCalled <- struct{}{}
			return nil, nil, errors.New("transport must not start")
		}, &stderr)
	}()
	waitForStatus(t, "http://"+cfg.ControlAddr+"/readyz", http.StatusServiceUnavailable)
	select {
	case <-factoryCalled:
		t.Fatal("transport started while an active legacy Run blocks migration")
	default:
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("diagnostic runtime shutdown: %v", err)
	}
	if !strings.Contains(stderr.String(), "active legacy run legacy-active") {
		t.Fatalf("missing active legacy diagnostic: %q", stderr.String())
	}
	after, err := os.ReadFile(legacyPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("legacy snapshot changed: err=%v", err)
	}
}

func TestCoorddTerminalLegacyRunIsNotImported(t *testing.T) {
	cfg := testConfig(t)
	legacyPath := filepath.Join(cfg.StateDir, "runs.json")
	terminal := fsm.Run{RunID: "legacy-done", Attempt: 1, Fence: 1, OwnerSession: "executor", Status: fsm.Done, LastUpdate: time.Now().UTC()}
	if err := (checkpoint.File{Path: legacyPath}).Save(map[string]fsm.Run{terminal.RunID: terminal}); err != nil {
		t.Fatal(err)
	}
	legacyBefore, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("stop after confirming terminal legacy state was accepted")
	err = runWithWriter(context.Background(), cfg, func(context.Context, coorddconfig.Config) (transport.Transport, func() error, error) {
		return nil, nil, want
	}, io.Discard)
	if !errors.Is(err, want) {
		t.Fatalf("terminal legacy Run prevented normal startup: %v", err)
	}
	legacyAfter, err := os.ReadFile(legacyPath)
	if err != nil || string(legacyAfter) != string(legacyBefore) {
		t.Fatalf("legacy snapshot changed: err=%v", err)
	}
	canonical, err := (checkpoint.CanonicalFile{Path: filepath.Join(cfg.StateDir, "canonical.json")}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical.Runs) != 0 {
		t.Fatalf("terminal legacy Run was imported into canonical state: %+v", canonical.Runs)
	}
}

func TestCoorddUnknownCanonicalSchemaKeepsNotReady(t *testing.T) {
	cfg := testConfig(t)
	cfg.ControlAddr, cfg.MetricsAddr = freeLoopbackAddrs(t)
	if err := os.MkdirAll(cfg.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.StateDir, "canonical.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":999,"tasks":{},"runs":{},"events":[],"inbox":{},"outbox":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	factoryCalled := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- runWithWriter(ctx, cfg, func(context.Context, coorddconfig.Config) (transport.Transport, func() error, error) {
			factoryCalled <- struct{}{}
			return nil, nil, errors.New("transport must not start")
		}, io.Discard)
	}()
	waitForStatus(t, "http://"+cfg.ControlAddr+"/readyz", http.StatusServiceUnavailable)
	select {
	case <-factoryCalled:
		t.Fatal("transport started with an unknown canonical schema")
	default:
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("diagnostic runtime shutdown: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("unknown canonical snapshot changed: err=%v", err)
	}
}

func TestCoorddReadinessRejectsInvalidCanonicalState(t *testing.T) {
	cfg := testConfig(t)
	cfg.ControlAddr, cfg.MetricsAddr = freeLoopbackAddrs(t)
	if err := os.MkdirAll(cfg.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.StateDir, "canonical.json")
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	now := time.Unix(100, 0).UTC()
	stage := task.Execution
	state := store.EmptyCanonicalState()
	state.Tasks[id] = task.Task{ID: id, Title: "task", OwnerID: "owner", Revision: 1, LastEventSeq: 2,
		StagePlan:    task.StagePlan{Stages: []task.Stage{task.Execution}, PolicyVersion: task.StagePlanPolicyVersion},
		CurrentStage: &stage, Status: task.Pending, CreatedAt: now, UpdatedAt: now}
	state.Events = []task.DomainEvent{{ID: task.EventID(id, 1), TaskID: id, EventSeq: 1, TaskRevision: 1,
		Type: task.TaskCreated, ActorID: "owner", CreatedAt: now}}
	seed, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, seed, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	factoryCalled := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- runWithWriter(ctx, cfg, func(context.Context, coorddconfig.Config) (transport.Transport, func() error, error) {
			factoryCalled <- struct{}{}
			return nil, nil, errors.New("transport must not start")
		}, io.Discard)
	}()
	waitForStatus(t, "http://"+cfg.ControlAddr+"/readyz", http.StatusServiceUnavailable)
	response, err := http.Get("http://" + cfg.ControlAddr + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || !strings.Contains(string(body), "event history does not reach last event sequence") {
		t.Fatalf("/readyz body=%q readErr=%v; want ValidateCanonicalState event-history failure", body, readErr)
	}
	select {
	case <-factoryCalled:
		t.Fatal("transport started with invalid canonical state")
	default:
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("diagnostic runtime shutdown: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("invalid canonical snapshot changed: err=%v", err)
	}
}

func TestCoorddReadinessFailsClosedForActiveTaskRunWithoutAgentSessionBinding(t *testing.T) {
	cfg := testConfig(t)
	cfg.ControlAddr, cfg.MetricsAddr = freeLoopbackAddrs(t)
	if err := os.MkdirAll(cfg.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.StateDir, "canonical.json")
	id := taskid.ID("task_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	now := time.Unix(100, 0).UTC()
	stage := task.Execution
	state := store.EmptyCanonicalState()
	state.Tasks[id] = task.Task{ID: id, Title: "task", OwnerID: "owner", Revision: 2, LastEventSeq: 2, Fence: 1,
		StagePlan:    task.StagePlan{Stages: []task.Stage{task.Execution}, PolicyVersion: task.StagePlanPolicyVersion},
		CurrentStage: &stage, Status: task.InProgress, CreatedAt: now, UpdatedAt: now.Add(time.Second)}
	selection := roles.Selection{AgentID: "agent-executor", Session: "executor", Profile: roles.Profile{Role: roles.Executor}}
	state.Runs["run-1"] = fsm.Run{TaskID: id, RunID: "run-1", Attempt: 1, Fence: 1, OwnerSession: selection.Session,
		Worktree: "worktree", Deadline: now.Add(time.Hour), Status: fsm.Stalled, ResumeStatus: fsm.Started,
		RecoveryNeeded: true, RoleSelection: &selection, RequiredRole: roles.Requirement{Role: roles.Executor}, RoleHistory: []roles.Selection{selection}}
	state.Events = []task.DomainEvent{
		{ID: task.EventID(id, 1), TaskID: id, EventSeq: 1, TaskRevision: 1, Type: task.TaskCreated, ActorID: "owner", CreatedAt: now},
		{ID: task.EventID(id, 2), TaskID: id, RunID: "run-1", EventSeq: 2, TaskRevision: 2, TaskFence: 1, Type: task.TaskOwnershipTransferred, CreatedAt: now.Add(time.Second)},
	}
	seed, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, seed, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	factoryCalled := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- runWithWriter(ctx, cfg, func(context.Context, coorddconfig.Config) (transport.Transport, func() error, error) {
			factoryCalled <- struct{}{}
			return nil, nil, errors.New("transport must not start")
		}, io.Discard)
	}()
	waitForStatus(t, "http://"+cfg.ControlAddr+"/readyz", http.StatusServiceUnavailable)
	response, err := http.Get("http://" + cfg.ControlAddr + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || !strings.Contains(string(body), "has no AgentSession generation") {
		t.Fatalf("/readyz body=%q readErr=%v; want missing AgentSession generation", body, readErr)
	}
	select {
	case <-factoryCalled:
		t.Fatal("transport started with active Task Run lacking AgentSession binding")
	default:
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("diagnostic runtime shutdown: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, seed) {
		t.Fatalf("invalid active Run snapshot was rewritten: err=%v", err)
	}
}

func waitForStatus(t *testing.T, url string, want int) {
	t.Helper()
	client := &http.Client{Timeout: 250 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(url)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == want {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("endpoint %s did not return status %d", url, want)
}

func TestCoorddTransportStartupFailureReleasesOwnership(t *testing.T) {
	cfg := testConfig(t)
	want := errors.New("transport startup failed")
	err := run(context.Background(), cfg, func(context.Context, coorddconfig.Config) (transport.Transport, func() error, error) {
		return nil, nil, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("run error = %v, want transport failure", err)
	}
	release, err := filelock.Lock(filepath.Join(cfg.StateDir, "namespace.lock"))
	if err != nil {
		t.Fatalf("namespace lock leaked after transport failure: %v", err)
	}
	_ = release()
}

func TestCoorddTransportFailureReportsReleasedOwnership(t *testing.T) {
	cfg := testConfig(t)
	path := writeTestConfig(t, cfg)
	var stderr strings.Builder
	want := errors.New("MCP initialize version mismatch")
	code := mainRunWithWriter([]string{"-config", path}, func(context.Context, coorddconfig.Config) (transport.Transport, func() error, error) {
		return nil, nil, want
	}, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "reason: transport:") || !strings.Contains(stderr.String(), "namespace lock: released") {
		t.Fatalf("post-lock transport failure report code=%d stderr=%q", code, stderr.String())
	}
	release, err := filelock.Lock(filepath.Join(cfg.StateDir, "namespace.lock"))
	if err != nil {
		t.Fatalf("transport startup failure leaked namespace lock: %v", err)
	}
	_ = release()
}

func TestDaemonGracefulShutdownReleasesNamespaceAfterHandlers(t *testing.T) {
	cfg := testConfig(t)
	cfg.ControlAddr, cfg.MetricsAddr = freeLoopbackAddrs(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, cfg, func(context.Context, coorddconfig.Config) (transport.Transport, func() error, error) {
			t := &idleTransport{session: "coordd", in: make(chan envelope.Envelope)}
			closeTransport := func() error {
				for _, path := range []string{
					filepath.Join(cfg.StateDir, "namespace.lock"),
					filepath.Join(cfg.StateDir, "runs.json.lock"),
					filepath.Join(cfg.StateDir, "canonical.json.lock"),
					filepath.Join(cfg.StateDir, "roles.json.lock"),
				} {
					release, err := filelock.Lock(path)
					if err == nil {
						_ = release()
						return fmt.Errorf("ownership released before transport shutdown: %s", path)
					}
					if !errors.Is(err, filelock.ErrLocked) {
						return err
					}
				}
				conn, err := net.DialTimeout("tcp", cfg.ControlAddr, 100*time.Millisecond)
				if err == nil {
					_ = conn.Close()
					return errors.New("control listener still accepted connections during transport close")
				}
				return nil
			}
			return t, closeTransport, nil
		})
	}()

	client := &http.Client{Timeout: 200 * time.Millisecond}
	readyURL := "http://" + cfg.ControlAddr + "/readyz"
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		response, err := client.Get(readyURL)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("daemon did not become ready: last error=%v", err)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("graceful shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not stop after SIGTERM-equivalent cancellation")
	}
	release, err := filelock.Lock(filepath.Join(cfg.StateDir, "namespace.lock"))
	if err != nil {
		t.Fatalf("namespace lock not released after daemon shutdown: %v", err)
	}
	_ = release()
}

func freeLoopbackAddrs(t *testing.T) (string, string) {
	t.Helper()
	control, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = control.Close()
		t.Fatal(err)
	}
	controlAddr, metricsAddr := control.Addr().String(), metrics.Addr().String()
	if err := control.Close(); err != nil {
		t.Fatal(err)
	}
	if err := metrics.Close(); err != nil {
		t.Fatal(err)
	}
	return controlAddr, metricsAddr
}

func TestStoreAndFilelockHelperProcess(t *testing.T) {
	if os.Getenv("COORDD_HELPER_PROCESS") != "1" {
		return
	}
	switch os.Getenv("COORDD_HELPER_MODE") {
	case "lock":
		path := os.Getenv("COORDD_HELPER_LOCK")
		release, err := filelock.Lock(path)
		if err != nil {
			_, _ = os.Stderr.WriteString(err.Error())
			os.Exit(3)
		}
		_ = release
		_, _ = os.Stdout.WriteString("LOCKED\n")
		for {
			runtime.KeepAlive(release)
			time.Sleep(time.Hour)
		}
	case "seed_run", "seed_result":
		processHelperCoordinator, processHelperStore = seedHelper(os.Getenv("COORDD_HELPER_STATE"), os.Getenv("COORDD_HELPER_MODE") == "seed_result")
		_, _ = os.Stdout.WriteString("SEEDED\n")
		for {
			runtime.KeepAlive(processHelperCoordinator)
			runtime.KeepAlive(processHelperStore)
			time.Sleep(time.Hour)
		}
	case "inspect":
		inspectHelper(os.Getenv("COORDD_HELPER_STATE"))
	case "replay":
		replayHelper(os.Getenv("COORDD_HELPER_STATE"))
	case "try_store":
		path := filepath.Join(os.Getenv("COORDD_HELPER_STATE"), "runs.json")
		snapshot, err := store.New(checkpoint.File{Path: path})
		if err != nil {
			_, _ = os.Stdout.WriteString("REFUSED\n")
			os.Exit(0)
		}
		_ = snapshot.Close()
		_, _ = os.Stdout.WriteString("OPENED\n")
	default:
		os.Exit(4)
	}
	os.Exit(0)
}

func TestFilelockProcessKillReleasesNamespaceLock(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "namespace.lock")
	child := exec.Command(os.Args[0], "-test.run=^TestStoreAndFilelockHelperProcess$")
	child.Env = append(os.Environ(), "COORDD_HELPER_PROCESS=1", "COORDD_HELPER_MODE=lock", "COORDD_HELPER_LOCK="+lockPath)
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	child.Stderr = &stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		_ = child.Process.Kill()
		_ = child.Wait()
		t.Fatalf("child did not report lock acquisition: %v (%s)", err, stderr.String())
	}
	if line != "LOCKED\n" {
		_ = child.Process.Kill()
		_ = child.Wait()
		t.Fatalf("unexpected helper output %q (%s)", line, stderr.String())
	}
	if release, err := filelock.Lock(lockPath); err == nil {
		_ = release()
		_ = child.Process.Kill()
		_ = child.Wait()
		t.Fatal("second process acquired namespace while owner was alive")
	} else if !errors.Is(err, filelock.ErrLocked) {
		_ = child.Process.Kill()
		_ = child.Wait()
		t.Fatalf("unexpected competing lock error: %v", err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("killed helper exited successfully")
	}
	release, err := filelock.Lock(lockPath)
	if err != nil {
		t.Fatalf("namespace ownership not released after process kill: %v", err)
	}
	_ = release()
}

func startDaemonHelper(t *testing.T, mode, stateDir string) (*exec.Cmd, *strings.Builder) {
	t.Helper()
	child := exec.Command(os.Args[0], "-test.run=^TestStoreAndFilelockHelperProcess$")
	child.Env = append(os.Environ(), "COORDD_HELPER_PROCESS=1", "COORDD_HELPER_MODE="+mode, "COORDD_HELPER_STATE="+stateDir)
	if mode == "lock" {
		child.Env = append(child.Env, "COORDD_HELPER_LOCK="+stateDir)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	child.Stderr = &stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		_ = child.Process.Kill()
		_ = child.Wait()
		t.Fatalf("helper %s failed before marker: %v (%s)", mode, err, stderr.String())
	}
	want := "SEEDED\n"
	if mode == "lock" {
		want = "LOCKED\n"
	}
	if line != want {
		_ = child.Process.Kill()
		_ = child.Wait()
		t.Fatalf("helper %s marker=%q, want %q; stderr=%s", mode, line, want, stderr.String())
	}
	return child, &stderr
}

func writeTestConfig(t *testing.T, cfg coorddconfig.Config) string {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(filepath.Dir(cfg.StateDir), "coordd-test.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func invokeDaemonHelper(t *testing.T, mode, stateDir string) string {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestStoreAndFilelockHelperProcess$")
	command.Env = append(os.Environ(), "COORDD_HELPER_PROCESS=1", "COORDD_HELPER_MODE="+mode, "COORDD_HELPER_STATE="+stateDir)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("helper %s: %v\n%s", mode, err, output)
	}
	return string(output)
}

func killDaemonHelper(t *testing.T, child *exec.Cmd) {
	t.Helper()
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("killed daemon helper exited successfully")
	}
}

func TestStoreSecondProcessCannotOpenWhileStoreOwnerRunning(t *testing.T) {
	stateDir := t.TempDir()
	owner, _ := startDaemonHelper(t, "seed_run", stateDir)
	defer func() { _ = owner.Process.Kill(); _ = owner.Wait() }()
	output := invokeDaemonHelper(t, "try_store", stateDir)
	if !strings.Contains(output, "REFUSED") {
		t.Fatalf("second process opened a writer while owner was live: %q", output)
	}
}

func TestStoreProcessRestartRestoresRunSnapshot(t *testing.T) {
	stateDir := t.TempDir()
	first, _ := startDaemonHelper(t, "seed_run", stateDir)
	killDaemonHelper(t, first)
	output := invokeDaemonHelper(t, "inspect", stateDir)
	if !strings.Contains(output, "RUN PENDING 1 3 outbox=1 result=-") {
		t.Fatalf("committed run was not restored after process restart: %q", output)
	}
}

func TestStoreProcessRestartReplaysOutbox(t *testing.T) {
	stateDir := t.TempDir()
	first, _ := startDaemonHelper(t, "seed_run", stateDir)
	killDaemonHelper(t, first)
	output := invokeDaemonHelper(t, "replay", stateDir)
	if !strings.Contains(output, "REPLAYED sent=1 outbox=0") {
		t.Fatalf("Run outbox was not replayed after restart: %q", output)
	}
}

func TestStoreProcessRestartPreservesCommittedResult(t *testing.T) {
	stateDir := t.TempDir()
	first, _ := startDaemonHelper(t, "seed_result", stateDir)
	killDaemonHelper(t, first)
	output := invokeDaemonHelper(t, "inspect", stateDir)
	if !strings.Contains(output, "RUN DONE 1 3 outbox=1 result=SUCCESS") {
		t.Fatalf("committed result/receipt was not preserved after restart: %q", output)
	}
}

func seedHelper(stateDir string, terminal bool) (*coord.Coordinator, *store.Store) {
	snapshot, err := store.New(checkpoint.File{Path: filepath.Join(stateDir, "runs.json")})
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(10)
	}
	tp := &idleTransport{session: "coord", in: make(chan envelope.Envelope), sendErr: errors.New("offline")}
	coordinator, err := coord.New(tp, snapshot, coord.DefaultConfig())
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(11)
	}
	now := time.Now().UTC()
	run := fsm.Run{Task: "local-task", RunID: "run-1", Attempt: 1, Fence: 3, TargetSHA: strings.Repeat("a", 40), OwnerSession: "agent", Worktree: stateDir, Deadline: now.Add(24 * time.Hour)}
	if err = coordinator.Assign(context.Background(), run); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(12)
	}
	if terminal {
		stored, _ := snapshot.Get(run.RunID)
		events := []envelope.Envelope{
			{Kind: envelope.Ack, AckPayload: &envelope.AckPayload{State: "ACCEPTED", NextUpdateBy: now.Add(time.Hour)}},
			{Kind: envelope.Heartbeat, HeartbeatPayload: &envelope.HeartbeatPayload{State: "STARTED", NextUpdateBy: now.Add(time.Hour)}},
			{Kind: envelope.Heartbeat, HeartbeatPayload: &envelope.HeartbeatPayload{State: "RESULT_READY", ProgressPct: 100, NextUpdateBy: now.Add(time.Hour)}},
			{Kind: envelope.Result, ResultPayload: &envelope.ResultPayload{Outcome: "SUCCESS", Summary: "committed result"}},
		}
		for i, incoming := range events {
			incoming.SchemaVersion = 1
			incoming.MessageID = envelope.NewID()
			incoming.InReplyTo = stored.TaskMessages[len(stored.TaskMessages)-1]
			incoming.Timestamp = now.Add(time.Duration(i+1) * time.Second)
			incoming.From, incoming.To = run.OwnerSession, tp.session
			incoming.Task, incoming.Run = run.Task, run.RunID
			incoming.Attempt, incoming.TargetSHA, incoming.Fence = run.Attempt, run.TargetSHA, run.Fence
			if err = coordinator.Handle(context.Background(), incoming); err != nil {
				_, _ = fmt.Fprintln(os.Stderr, err)
				os.Exit(13)
			}
			stored, _ = snapshot.Get(run.RunID)
		}
	}
	return coordinator, snapshot
}

func inspectHelper(stateDir string) {
	snapshot, err := store.New(checkpoint.File{Path: filepath.Join(stateDir, "runs.json")})
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(20)
	}
	run, ok := snapshot.Get("run-1")
	if !ok {
		_, _ = fmt.Fprintln(os.Stderr, "committed run missing")
		os.Exit(21)
	}
	outcome := "-"
	if run.Result != nil {
		outcome = run.Result.Outcome
	}
	_, _ = fmt.Fprintf(os.Stdout, "RUN %s %d %d outbox=%d result=%s\n", run.Status, run.Attempt, run.Fence, len(run.Outbox), outcome)
	_ = snapshot.Close()
}

func replayHelper(stateDir string) {
	snapshot, err := store.New(checkpoint.File{Path: filepath.Join(stateDir, "runs.json")})
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(30)
	}
	tp := &idleTransport{session: "coord", in: make(chan envelope.Envelope)}
	config := coord.DefaultConfig()
	config.Now = func() time.Time { return time.Now().UTC().Add(time.Hour) }
	coordinator, err := coord.New(tp, snapshot, config)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(31)
	}
	if err = coordinator.Tick(context.Background()); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(32)
	}
	run, _ := snapshot.Get("run-1")
	_, _ = fmt.Fprintf(os.Stdout, "REPLAYED sent=%d outbox=%d status=%s\n", tp.sent, len(run.Outbox), run.Status)
	_ = coordinator.Close()
}
