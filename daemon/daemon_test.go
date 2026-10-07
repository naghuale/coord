package daemon

import (
	"context"
	"coord"
	"coord/checkpoint"
	"coord/envelope"
	"coord/fsm"
	"coord/internal/filelock"
	"coord/store"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryPersistence struct {
	mu   sync.Mutex
	runs map[string]fsm.Run
}

type closeFailPersistence struct {
	memoryPersistence
	closeErr error
}

func (p *closeFailPersistence) Lock() (func() error, error) {
	return func() error { return p.closeErr }, nil
}

func (p *memoryPersistence) Load() (map[string]fsm.Run, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]fsm.Run, len(p.runs))
	for id, run := range p.runs {
		out[id] = run
	}
	return out, nil
}

func (p *memoryPersistence) Save(runs map[string]fsm.Run) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.runs = make(map[string]fsm.Run, len(runs))
	for id, run := range runs {
		p.runs[id] = run
	}
	return nil
}

type testTransport struct {
	inbox          chan envelope.Envelope
	receiveGate    <-chan struct{}
	receiveStarted chan struct{}
	startOnce      sync.Once
}

func (t *testTransport) Send(context.Context, string, envelope.Envelope) error { return nil }
func (t *testTransport) Receive(ctx context.Context) <-chan envelope.Envelope {
	t.startOnce.Do(func() {
		if t.receiveStarted != nil {
			close(t.receiveStarted)
		}
	})
	if t.receiveGate != nil {
		select {
		case <-t.receiveGate:
		case <-ctx.Done():
			return nil
		}
	}
	return t.inbox
}
func (t *testTransport) WhoAmI() string { return "daemon-test" }

func TestDaemonReadinessLifecycle(t *testing.T) {
	persistence := &memoryPersistence{runs: map[string]fsm.Run{}}
	state, err := store.New(persistence)
	if err != nil {
		t.Fatal(err)
	}
	receiveGate := make(chan struct{})
	receiveStarted := make(chan struct{})
	transport := &testTransport{inbox: make(chan envelope.Envelope), receiveGate: receiveGate, receiveStarted: receiveStarted}
	coordinator, err := coord.New(transport, state, coord.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.ControlAddr = "127.0.0.1:0"
	cfg.MetricsAddr = "127.0.0.1:0"
	cfg.ControlToken = "test-token"
	cfg.ShutdownTimeout = 5 * time.Second
	d, err := New(coordinator, cfg)
	if err != nil {
		_ = coordinator.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.close() })

	client := &http.Client{Timeout: 100 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	select {
	case <-receiveStarted:
	case <-time.After(time.Second):
		t.Fatal("coordinator did not initialize transport receive")
	}
	waitStatus(t, client, "http://"+d.MetricsAddr()+"/readyz", http.StatusServiceUnavailable)
	request, err := http.NewRequest(http.MethodPost, "http://"+d.ControlAddr()+"/claim", strings.NewReader(`{"run":"none","fence":1,"claim":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("pre-ready control status = %d, want 503", response.StatusCode)
	}
	closeBody(t, response)
	close(receiveGate)
	waitReady(t, client, "http://"+d.MetricsAddr()+"/readyz")
	response = get(t, client, "http://"+d.MetricsAddr()+"/")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d, want 200", response.StatusCode)
	}
	closeBody(t, response)
	request, err = http.NewRequest(http.MethodGet, "http://"+d.ControlAddr()+"/runs", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer test-token")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("ready control status = %d, want 200", response.StatusCode)
	}
	closeBody(t, response)

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("daemon shutdown: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("daemon did not stop after context cancellation")
	}
	if err := state.Update(func(map[string]fsm.Run) error { return nil }); err == nil || !strings.Contains(err.Error(), "store closed") {
		t.Fatalf("store after daemon shutdown error = %v, want closed", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close after Run returned: %v", err)
	}
	if err := d.Run(context.Background()); err == nil {
		t.Fatal("Run after shutdown unexpectedly succeeded")
	}
}

func TestDaemonReopensFileBackedRunSnapshotAndReleasesLock(t *testing.T) {
	snapshot := checkpoint.File{Path: filepath.Join(t.TempDir(), "runs.json")}
	firstStore, err := store.New(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	firstTransport := &testTransport{inbox: make(chan envelope.Envelope)}
	firstCoordinator, err := coord.New(firstTransport, firstStore, coord.DefaultConfig())
	if err != nil {
		_ = firstStore.Close()
		t.Fatal(err)
	}
	run := fsm.Run{
		Task: "LOCAL-001", RunID: "LOCAL-001-run-1", Attempt: 1, Fence: 1,
		OwnerSession: "worker", TargetSHA: strings.Repeat("a", 40),
		Worktree: t.TempDir(), Deadline: time.Now().Add(time.Hour),
	}
	if err := firstCoordinator.Assign(context.Background(), run); err != nil {
		_ = firstCoordinator.Close()
		t.Fatal(err)
	}
	if err := firstCoordinator.Close(); err != nil {
		t.Fatal(err)
	}

	restoredStore, err := store.New(snapshot)
	if err != nil {
		t.Fatalf("reopen snapshot: %v", err)
	}
	secondTransport := &testTransport{inbox: make(chan envelope.Envelope)}
	restoredCoordinator, err := coord.New(secondTransport, restoredStore, coord.DefaultConfig())
	if err != nil {
		_ = restoredStore.Close()
		t.Fatal(err)
	}
	restored, ok := restoredStore.Get(run.RunID)
	if !ok || restored.Status != fsm.Pending || restored.Task != run.Task || restored.Attempt != run.Attempt || restored.Fence != run.Fence || restored.OwnerSession != run.OwnerSession {
		_ = restoredCoordinator.Close()
		t.Fatalf("restored run does not match assigned snapshot: found=%t run=%+v", ok, restored)
	}

	cfg := DefaultConfig()
	cfg.ControlAddr = "127.0.0.1:0"
	cfg.MetricsAddr = "127.0.0.1:0"
	cfg.ControlToken = "test-token"
	cfg.ShutdownTimeout = 5 * time.Second
	d, err := New(restoredCoordinator, cfg)
	if err != nil {
		_ = restoredCoordinator.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	client := &http.Client{Timeout: time.Second}
	waitReady(t, client, "http://"+d.MetricsAddr()+"/readyz")
	if competingStore, err := store.New(snapshot); err == nil {
		_ = competingStore.Close()
		t.Fatal("second store acquired the active daemon snapshot lock")
	} else if !errors.Is(err, filelock.ErrLocked) {
		t.Fatalf("second store error = %v, want ErrLocked", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("restarted daemon shutdown: %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("restarted daemon did not shut down")
	}

	finalStore, err := store.New(snapshot)
	if err != nil {
		t.Fatalf("snapshot lock was not released after daemon shutdown: %v", err)
	}
	t.Cleanup(func() {
		if err := finalStore.Close(); err != nil {
			t.Errorf("close verification store: %v", err)
		}
	})
	final, ok := finalStore.Get(run.RunID)
	if !ok || final.Status != fsm.Pending || final.Task != run.Task {
		t.Fatalf("shutdown changed restored snapshot: found=%t run=%+v", ok, final)
	}
}

func TestDaemonReturnsCoordinatorFailureWithoutShutdownTimeout(t *testing.T) {
	state, err := store.New(&memoryPersistence{runs: map[string]fsm.Run{}})
	if err != nil {
		t.Fatal(err)
	}
	transport := &testTransport{inbox: make(chan envelope.Envelope)}
	coordinator, err := coord.New(transport, state, coord.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.ControlAddr = "127.0.0.1:0"
	cfg.MetricsAddr = "127.0.0.1:0"
	cfg.ControlToken = "test-token"
	cfg.ShutdownTimeout = 5 * time.Second
	d, err := New(coordinator, cfg)
	if err != nil {
		_ = coordinator.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.close() })

	done := make(chan error, 1)
	go func() { done <- d.Run(context.Background()) }()
	waitReady(t, &http.Client{Timeout: time.Second}, "http://"+d.MetricsAddr()+"/readyz")
	close(transport.inbox)
	select {
	case err := <-done:
		if !errors.Is(err, coord.ErrTransportClosed) {
			t.Fatalf("daemon error = %v, want ErrTransportClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon waited for shutdown timeout after coordinator exited")
	}
}

func TestDaemonReturnsCoordinatorStoreCloseError(t *testing.T) {
	closeErr := errors.New("snapshot lock close failed")
	persistence := &closeFailPersistence{
		memoryPersistence: memoryPersistence{runs: map[string]fsm.Run{}},
		closeErr:          closeErr,
	}
	state, err := store.New(persistence)
	if err != nil {
		t.Fatal(err)
	}
	transport := &testTransport{inbox: make(chan envelope.Envelope)}
	coordinator, err := coord.New(transport, state, coord.DefaultConfig())
	if err != nil {
		_ = state.Close()
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.ControlAddr = "127.0.0.1:0"
	cfg.MetricsAddr = "127.0.0.1:0"
	cfg.ControlToken = "test-token"
	d, err := New(coordinator, cfg)
	if err != nil {
		_ = coordinator.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	waitReady(t, &http.Client{Timeout: time.Second}, "http://"+d.MetricsAddr()+"/readyz")
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, closeErr) {
			t.Fatalf("daemon error = %v, want store close error %v", err, closeErr)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("daemon did not stop after context cancellation")
	}
}

func TestDaemonKeepsRecoveryControlAvailableUntilRecoveryCompletes(t *testing.T) {
	now := time.Now().UTC()
	run := fsm.Run{
		Task: "CF-009", RunID: "run", Attempt: 1, TargetSHA: strings.Repeat("a", 40),
		OwnerSession: "executor", Fence: 7, Status: fsm.Stalled, ResumeStatus: fsm.Started,
		Worktree: "/worktree", Deadline: now.Add(time.Hour), RecoveryNeeded: true,
	}
	state, err := store.New(&memoryPersistence{runs: map[string]fsm.Run{"run": run}})
	if err != nil {
		t.Fatal(err)
	}
	transport := &testTransport{inbox: make(chan envelope.Envelope)}
	coordinator, err := coord.New(transport, state, coord.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.ControlAddr = "127.0.0.1:0"
	cfg.MetricsAddr = "127.0.0.1:0"
	cfg.ControlToken = "test-token"
	cfg.ShutdownTimeout = 5 * time.Second
	d, err := New(coordinator, cfg)
	if err != nil {
		_ = coordinator.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.close() })

	done := make(chan error, 1)
	go func() { done <- d.Run(context.Background()) }()
	client := &http.Client{Timeout: time.Second}
	controlURL := "http://" + d.ControlAddr()
	waitReadyControl(t, client, controlURL+"/runs", cfg.ControlToken)
	response := get(t, client, "http://"+d.MetricsAddr()+"/readyz")
	body, readErr := io.ReadAll(response.Body)
	closeBody(t, response)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "supervisor_recovery_pending") {
		t.Fatalf("pending recovery readiness = %d %q", response.StatusCode, body)
	}

	postControl(t, client, controlURL+"/claim", cfg.ControlToken, `{"run":"run","fence":7,"claim":"watchdog-1"}`, http.StatusNoContent)
	postControl(t, client, controlURL+"/fail", cfg.ControlToken, `{"run":"run","fence":7,"claim":"watchdog-1","reason":"supervisor confirmed stop"}`, http.StatusNoContent)
	waitReady(t, client, "http://"+d.MetricsAddr()+"/readyz")

	// The test runtime is stopped by closing its transport inbox.
	close(transport.inbox)
	select {
	case err := <-done:
		if !errors.Is(err, coord.ErrTransportClosed) {
			t.Fatalf("daemon error = %v, want ErrTransportClosed", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("daemon did not stop after transport closed")
	}
}

func TestDaemonRejectsNonLoopbackListeners(t *testing.T) {
	state, err := store.New(&memoryPersistence{runs: map[string]fsm.Run{}})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := coord.New(&testTransport{inbox: make(chan envelope.Envelope)}, state, coord.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.ControlAddr = "0.0.0.0:8090"
	cfg.MetricsAddr = "127.0.0.1:0"
	cfg.ControlToken = "test-token"
	if _, err := New(coordinator, cfg); err == nil {
		_ = coordinator.Close()
		t.Fatal("non-loopback control listener was accepted without TLS support")
	}
	if err := coordinator.Close(); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	response, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func waitReady(t *testing.T, client *http.Client, url string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(url)
		if err == nil {
			closeBody(t, response)
			if response.StatusCode == http.StatusOK {
				return
			}
		} else if !strings.Contains(err.Error(), "connection refused") && !strings.Contains(err.Error(), "Client.Timeout") {
			t.Fatal(fmt.Errorf("readiness request: %w", err))
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("daemon did not become ready")
}

func waitStatus(t *testing.T, client *http.Client, url string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(url)
		if err == nil {
			closeBody(t, response)
			if response.StatusCode == want {
				return
			}
			if response.StatusCode == http.StatusOK && want != http.StatusOK {
				t.Fatalf("status became ready before receive initialization; got %d", response.StatusCode)
			}
		} else if !strings.Contains(err.Error(), "connection refused") && !strings.Contains(err.Error(), "Client.Timeout") {
			t.Fatal(fmt.Errorf("request before readiness: %w", err))
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server never returned status %d before timeout", want)
}

func closeBody(t *testing.T, response *http.Response) {
	t.Helper()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Errorf("drain response body: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Errorf("close response body: %v", err)
	}
}

func waitReadyControl(t *testing.T, client *http.Client, url, token string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		request, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := client.Do(request)
		if err == nil {
			closeBody(t, response)
			if response.StatusCode == http.StatusOK {
				return
			}
		} else if !strings.Contains(err.Error(), "connection refused") {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("recovery control endpoint did not become available")
}

func postControl(t *testing.T, client *http.Client, url, token, body string, want int) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer closeBody(t, response)
	if response.StatusCode != want {
		t.Fatalf("POST %s status = %d, want %d", url, response.StatusCode, want)
	}
}
