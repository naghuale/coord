//go:build unix

// Package telepathyhost owns standalone Telepathy processes and local snapshots.
// It does not read or create canonical Crewflow Runs or Attempts.
package telepathyhost

import (
	"bytes"
	"context"
	"coord/envelope"
	"coord/fsm"
	"coord/telepathy"
	"coord/transport"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config binds one standalone run to explicit transport and execution identities.
// PrivateKey is a base64 Ed25519 key; protect the config as local credential state.
type Config struct {
	ExitOnTerminal bool              `json:"exit_on_terminal"`
	Role           string            `json:"role"`
	Server         string            `json:"server"`
	Home           string            `json:"home"`
	Directory      string            `json:"directory"`
	State          string            `json:"state"`
	AnchorPID      int               `json:"anchor_pid"`
	Session        string            `json:"session"`
	Coordinator    string            `json:"coordinator"`
	PrivateKey     string            `json:"private_key"`
	Peers          map[string]string `json:"peers"`
	Task           fsm.Run           `json:"task"`
	Prompt         string            `json:"prompt"`
	Command        []string          `json:"command"`
}

// Load refuses misspelled configuration fields and trailing JSON.
func Load(path string) (Config, error) {
	var cfg Config
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(&cfg); err != nil {
		return cfg, err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return cfg, fmt.Errorf("trailing config JSON")
	}
	return cfg, cfg.validate()
}
func (c Config) validate() error {
	if c.Role != "host" && c.Role != "worker" {
		return fmt.Errorf("role must be host or worker")
	}
	for _, path := range []string{c.Server, c.Home, c.Directory, c.State, c.Task.Worktree} {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("server, home, directory, state and worktree must be absolute")
		}
	}
	if c.AnchorPID <= 0 || c.Session != "opencode-"+strconv.Itoa(c.AnchorPID) || c.Coordinator == "" {
		return fmt.Errorf("session must equal opencode-<anchor_pid>; coordinator required")
	}
	if c.Role == "host" && c.Coordinator != c.Session {
		return fmt.Errorf("host must own coordinator identity")
	}
	if c.Role == "worker" && (c.Task.OwnerSession != c.Session || c.Prompt == "" || len(c.Command) == 0) {
		return fmt.Errorf("worker identity, prompt and command required")
	}
	if c.Task.ProgressPct != 0 || c.Task.RoleSelection != nil {
		return fmt.Errorf("standalone assignment requires zero initial progress and no dynamic role")
	}
	e := envelope.Envelope{SchemaVersion: 1, MessageID: "config", Timestamp: time.Now(), From: c.Coordinator, To: c.Task.OwnerSession, Kind: envelope.Task, Task: c.Task.Task, Run: c.Task.RunID, Attempt: c.Task.Attempt, TargetSHA: c.Task.TargetSHA, Fence: c.Task.Fence, TaskPayload: &envelope.TaskPayload{Worktree: c.Task.Worktree, PayloadRef: c.Task.PayloadRef, Deadline: c.Task.Deadline}}
	if err := e.Validate(); err != nil {
		return fmt.Errorf("task config: %w", err)
	}
	private, err := base64.StdEncoding.DecodeString(c.PrivateKey)
	if err != nil || len(private) != ed25519.PrivateKeySize {
		return fmt.Errorf("invalid private_key")
	}
	if !bytes.Equal(private, ed25519.NewKeyFromSeed(private[:ed25519.SeedSize])) {
		return fmt.Errorf("inconsistent private_key")
	}
	if len(c.Peers) != 1 {
		return fmt.Errorf("one pinned protocol peer is required")
	}
	peer := c.Coordinator
	if c.Role == "host" {
		peer = c.Task.OwnerSession
	}
	public, err := base64.StdEncoding.DecodeString(c.Peers[peer])
	if err != nil || len(public) != ed25519.PublicKeySize {
		return fmt.Errorf("missing pinned peer public key")
	}
	return nil
}
func connect(ctx context.Context, c Config) (*telepathy.Process, *telepathy.Client, *transport.Adapter, error) {
	environment := []string{}
	for _, s := range os.Environ() {
		if !strings.HasPrefix(s, "TELEPATHY_HOME=") && !strings.HasPrefix(s, "TELEPATHY_AGENT_PID=") {
			environment = append(environment, s)
		}
	}
	environment = append(environment, "TELEPATHY_HOME="+c.Home, "TELEPATHY_AGENT_PID="+strconv.Itoa(c.AnchorPID))
	p, err := telepathy.Start(ctx, []string{"node", c.Server, "--agent", "opencode"}, c.Directory, environment)
	if err != nil {
		return nil, nil, nil, err
	}
	listed, err := p.Call(ctx, "list_peers", map[string]any{})
	if err != nil {
		_ = p.Close()
		return nil, nil, nil, err
	}
	first, _, _ := strings.Cut(listed, "\n")
	if !strings.Contains(first, "["+c.Session+"]") {
		_ = p.Close()
		return nil, nil, nil, fmt.Errorf("actual Telepathy identity differs: %s", first)
	}
	client, err := telepathy.NewClient(p, c.Session, time.Second)
	if err != nil {
		_ = p.Close()
		return nil, nil, nil, err
	}
	private, _ := base64.StdEncoding.DecodeString(c.PrivateKey)
	keys := map[string]ed25519.PublicKey{}
	for session, key := range c.Peers {
		raw, _ := base64.StdEncoding.DecodeString(key)
		keys[session] = ed25519.PublicKey(raw)
	}
	signed := &transport.SignedClient{Client: client, PrivateKey: ed25519.PrivateKey(private), Peers: keys}
	return p, client, &transport.Adapter{Client: signed, Session: c.Session}, nil
}
