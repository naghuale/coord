//go:build unix

// Temporary experiment harness. Not a production Telepathy adapter.
package main

import (
	"bufio"
	"context"
	"coord"
	"coord/checkpoint"
	"coord/envelope"
	"coord/fsm"
	"coord/store"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type rpc struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	in      io.WriteCloser
	out     chan map[string]json.RawMessage
	id      int
	version string
}

func newRPC(ctx context.Context, server, home, dir string, pid int) (*rpc, error) {
	cmd := exec.CommandContext(ctx, "node", server, "--agent", "opencode")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TELEPATHY_HOME="+home, "TELEPATHY_AGENT_PID="+strconv.Itoa(pid))
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	r := &rpc{cmd: cmd, in: in, out: make(chan map[string]json.RawMessage, 32)}
	go func() {
		defer close(r.out)
		scan := bufio.NewScanner(out)
		scan.Buffer(make([]byte, 4096), 2<<20)
		for scan.Scan() {
			var msg map[string]json.RawMessage
			if json.Unmarshal(scan.Bytes(), &msg) == nil {
				r.out <- msg
			}
		}
	}()
	initialized, err := r.call(ctx, "initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "coord-isolated-experiment", "version": "0"}})
	if err != nil {
		r.close()
		return nil, err
	}
	var info struct {
		ServerInfo struct{ Name, Version string } `json:"serverInfo"`
	}
	if err = json.Unmarshal(initialized, &info); err != nil {
		r.close()
		return nil, err
	}
	if info.ServerInfo.Name != "telepathy" || info.ServerInfo.Version != "0.8.0" {
		r.close()
		return nil, fmt.Errorf("experiment requires Telepathy 0.8.0, got %s %s", info.ServerInfo.Name, info.ServerInfo.Version)
	}
	r.version = info.ServerInfo.Version
	_, err = fmt.Fprintln(in, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if err != nil {
		r.close()
		return nil, err
	}
	return r, nil
}
func (r *rpc) close() { _ = r.in.Close(); _ = r.cmd.Process.Kill(); _ = r.cmd.Wait() }
func (r *rpc) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.id++
	id := r.id
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	if _, err = fmt.Fprintln(r.in, string(b)); err != nil {
		return nil, err
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case m, ok := <-r.out:
			if !ok {
				return nil, errors.New("MCP stdout closed")
			}
			var got int
			_ = json.Unmarshal(m["id"], &got)
			if got != id {
				continue
			}
			if raw := m["error"]; len(raw) > 0 {
				return nil, fmt.Errorf("MCP error %s", raw)
			}
			return m["result"], nil
		}
	}
}
func (r *rpc) tool(ctx context.Context, name string, args any) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := r.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", err
	}
	var result struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		return "", err
	}
	parts := []string{}
	for _, c := range result.Content {
		if c.Type == "text" {
			parts = append(parts, c.Text)
		}
	}
	s := strings.Join(parts, "\n")
	if result.IsError {
		return s, errors.New(s)
	}
	return s, nil
}

var blocks = regexp.MustCompile(`(?s)BEGIN_COORD_ENVELOPE\n(.*?)\nEND_COORD_ENVELOPE`)

type tp struct {
	r       *rpc
	self    string
	log     *os.File
	logMu   sync.Mutex
	capture envelope.Envelope
}

func (t *tp) WhoAmI() string { return t.self }
func (t *tp) event(kind string, fields map[string]any) {
	t.logMu.Lock()
	defer t.logMu.Unlock()
	if fields == nil {
		fields = map[string]any{}
	}
	fields["event"] = kind
	fields["at"] = time.Now().UTC()
	_ = json.NewEncoder(t.log).Encode(fields)
	_ = t.log.Sync()
}
func (t *tp) Send(ctx context.Context, to string, e envelope.Envelope) error {
	if err := e.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	body := "BEGIN_COORD_ENVELOPE\n" + string(raw) + "\nEND_COORD_ENVELOPE"
	status, err := t.r.tool(ctx, "send_message", map[string]any{"to": to, "message": body})
	t.event("send", map[string]any{"kind": e.Kind, "mid": e.MessageID, "reply": e.InReplyTo, "attempt": e.Attempt, "fence": e.Fence, "to": to, "status": status, "ok": err == nil})
	if e.Kind == envelope.Task {
		t.capture = e
	}
	return err
}
func (t *tp) read(ctx context.Context) ([]envelope.Envelope, error) {
	s, err := t.r.tool(ctx, "read_messages", map[string]any{})
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(s, "No new messages.") || strings.HasPrefix(s, "No messages received yet.") {
		return nil, nil
	}
	out := []envelope.Envelope{}
	for _, m := range blocks.FindAllStringSubmatch(s, -1) {
		e, err := envelope.Decode([]byte(m[1]))
		if err != nil {
			return nil, err
		}
		if e.To != t.self {
			return nil, fmt.Errorf("wrong recipient")
		}
		out = append(out, e)
		t.event("receive", map[string]any{"kind": e.Kind, "mid": e.MessageID, "attempt": e.Attempt, "fence": e.Fence})
	}
	return out, nil
}
func (t *tp) Receive(ctx context.Context) <-chan envelope.Envelope {
	in := make(chan envelope.Envelope, 16)
	go func() {
		defer close(in)
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			msgs, err := t.read(ctx)
			if err != nil {
				t.event("read_error", map[string]any{"error": err.Error()})
				return
			}
			for _, e := range msgs {
				select {
				case in <- e:
				case <-ctx.Done():
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	return in
}

func save(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".checkpoint-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

type agentState struct {
	Task               envelope.Envelope
	Ack, Ready, Result envelope.Envelope
	WorkCount          int
}

func reply(task envelope.Envelope, k envelope.Kind) envelope.Envelope {
	e := task
	e.Kind = k
	e.TaskPayload = nil
	e.MessageID = envelope.NewID()
	e.InReplyTo = task.MessageID
	e.Timestamp = time.Now().UTC()
	e.From, e.To = task.To, task.From
	return e
}
func agent(ctx context.Context, t *tp, dir string, drop bool) error {
	state := agentState{}
	path := filepath.Join(dir, "agent-checkpoint.json")
	if b, err := os.ReadFile(path); err == nil {
		if err = json.Unmarshal(b, &state); err != nil {
			return err
		}
		t.event("checkpoint_loaded", map[string]any{"work_count": state.WorkCount, "mid": state.Task.MessageID})
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-ready"), []byte(t.self), 0600); err != nil {
		return err
	}
	for {
		msgs, err := t.read(ctx)
		if err != nil {
			return err
		}
		for _, task := range msgs {
			if task.Kind != envelope.Task {
				continue
			}
			if state.Task.Run != "" && (state.Task.Run != task.Run || state.Task.Task != task.Task || state.Task.Attempt != task.Attempt || state.Task.Fence != task.Fence || state.Task.TargetSHA != task.TargetSHA) {
				return fmt.Errorf("conflicting TASK identity")
			}
			state.Task = task
			if err = save(path, state); err != nil {
				return err
			}
			t.event("checkpoint_saved", map[string]any{"mid": task.MessageID, "work_count": state.WorkCount})
			if drop {
				if err = os.WriteFile(filepath.Join(dir, "crash-window-ready"), []byte(task.MessageID), 0600); err != nil {
					return err
				}
				<-ctx.Done()
				return ctx.Err()
			}
			if state.WorkCount > 0 {
				t.event("duplicate_task_no_execution", map[string]any{"work_count": state.WorkCount})
				continue
			}
			ack := reply(task, envelope.Ack)
			ack.AckPayload = &envelope.AckPayload{State: "ACCEPTED", NextUpdateBy: time.Now().Add(time.Minute)}
			state.Ack = ack
			if err = save(path, state); err != nil {
				return err
			}
			if err = t.Send(ctx, ack.To, ack); err != nil {
				return err
			}
			state.WorkCount++
			if err = save(path, state); err != nil {
				return err
			}
			t.event("scripted_work", map[string]any{"work_count": state.WorkCount})
			ready := reply(task, envelope.Heartbeat)
			ready.HeartbeatPayload = &envelope.HeartbeatPayload{State: "RESULT_READY", ProgressPct: 100, NextUpdateBy: time.Now().Add(time.Minute)}
			state.Ready = ready
			result := reply(task, envelope.Result)
			result.ResultPayload = &envelope.ResultPayload{Outcome: "SUCCESS", Summary: "isolated MCP experiment: one scripted operation"}
			state.Result = result
			if err = save(path, state); err != nil {
				return err
			}
			if err = t.Send(ctx, ready.To, ready); err != nil {
				return err
			}
			if err = t.Send(ctx, result.To, result); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
func coordinator(ctx context.Context, t *tp, dir, to, sha string) error {
	s, err := store.New(checkpoint.File{Path: filepath.Join(dir, "runs.json")})
	if err != nil {
		return err
	}
	defer s.Close()
	cfg := coord.DefaultConfig()
	cfg.AckTimeout = 3 * time.Second
	cfg.TickInterval = 100 * time.Millisecond
	cfg.ProbeTimeout = time.Second
	cfg.Retry.BaseDelay = time.Second
	cfg.Retry.MaxDelay = 3 * time.Second
	cfg.Retry.MaxRetries = 5
	cfg.Retry.Jitter = false
	c, err := coord.New(t, s, cfg)
	if err != nil {
		return err
	}
	run := fsm.Run{Task: "MCP-ROUNDTRIP", RunID: "MCP-ROUNDTRIP-run-1", Attempt: 3, Fence: 1, TargetSHA: sha, OwnerSession: to, Worktree: dir, Deadline: time.Now().Add(time.Minute)}
	// Run starts after initial direct transport checks to keep MCP calls serialized.
	if err = c.Assign(ctx, run); err != nil {
		return err
	}
	first := t.capture
	if err = t.Send(ctx, first.To, first); err == nil {
		return fmt.Errorf("expected exact-body duplicate rejection")
	}
	t.event("exact_duplicate_rejected", nil)
	done := make(chan error, 1)
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { done <- c.Run(child) }()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	last := fsm.Status("")
	for {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			r, _ := s.Get(run.RunID)
			if r.Status != last {
				last = r.Status
				t.event("fsm", map[string]any{"status": r.Status, "attempt": r.Attempt, "fence": r.Fence, "owner": r.OwnerSession})
			}
			if r.Status == fsm.Done {
				cancel()
				<-done
				// A semantic duplicate TASK has a new mid but the same work identity.
				duplicate := first
				duplicate.MessageID = envelope.NewID()
				duplicate.Timestamp = time.Now().UTC()
				if err = t.Send(ctx, to, duplicate); err != nil {
					return err
				}
				if err = os.WriteFile(filepath.Join(dir, "coordinator-done"), []byte("DONE"), 0600); err != nil {
					return err
				}
				return nil
			}
		}
	}
}
func main() {
	mode := flag.String("mode", "", "coordinator or agent")
	dir := flag.String("state", "", "isolated state directory")
	server := flag.String("server", "", "installed Telepathy server.mjs")
	pid := flag.Int("peer-pid", 0, "stable test session anchor PID; default own PID")
	to := flag.String("to", "", "recipient peer ref")
	sha := flag.String("sha", "", "full target SHA")
	newOwner := flag.String("replacement", "", "replacement peer ref for P0")
	worker := flag.String("worker", "", "test-only Python subprocess script")
	drop := flag.Bool("drop-ack", false, "wait for harness crash after durable TASK checkpoint")
	flag.Parse()
	if *pid == 0 {
		*pid = os.Getpid()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := os.MkdirAll(*dir, 0700); err != nil {
		panic(err)
	}
	log, err := os.OpenFile(filepath.Join(*dir, *mode+"-events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	defer log.Close()
	rpc, err := newRPC(ctx, *server, filepath.Join(*dir, "telepathy-home"), *dir, *pid)
	if err != nil {
		panic(err)
	}
	defer rpc.close()
	t := &tp{r: rpc, self: "opencode-" + strconv.Itoa(*pid), log: log}
	t.event("started", map[string]any{"pid": os.Getpid(), "peer": t.self, "mode": *mode, "telepathy": rpc.version})
	peers, err := rpc.tool(ctx, "list_peers", map[string]any{})
	if err != nil {
		panic(err)
	}
	t.event("peers", map[string]any{"text": peers})
	switch *mode {
	case "p2-agent":
		err = p2Agent(ctx, t, *dir, *worker)
	case "p2-coordinator":
		err = p2Coord(ctx, t, *dir, *to, *sha)
	case "p0-oldwriter":
		err = p0Writer(ctx, t, *dir, true)
	case "p0-newwriter":
		err = p0Writer(ctx, t, *dir, false)
	case "p0-late":
		err = p0Late(ctx, t, *dir)
	case "p0-coordinator":
		err = p0Coord(ctx, t, *dir, *to, *newOwner, *sha)
	case "p1-agent":
		err = p1Agent(ctx, t, *dir)
	case "p1-coordinator":
		err = p1Coord(ctx, t, *dir, *to, *sha)
	case "agent":
		err = agent(ctx, t, *dir, *drop)
	case "coordinator":
		err = coordinator(ctx, t, *dir, *to, *sha)
	default:
		err = errors.New("invalid mode")
	}
	if err != nil {
		t.event("failed", map[string]any{"error": err.Error()})
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	t.event("finished", nil)
}
