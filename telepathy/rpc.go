package telepathy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Process owns one MCP stdio subprocess, not an agent frontend.
type Process struct {
	cmd     *exec.Cmd
	input   io.WriteCloser
	output  chan response
	gate    chan struct{}
	done    chan struct{}
	cancel  context.CancelFunc
	once    sync.Once
	waitErr error
	// selfClosed records that this process stopped because Close cancelled it,
	// not because the parent context expired. It is written inside the same
	// sync.Once as cancel and read only after p.done, so no lock is needed.
	selfClosed bool
	id         uint64
}
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
	failure error
}

// Start launches an explicitly configured executable without a shell. Environment
// must include an isolated or deliberately selected TELEPATHY_HOME and anchor PID.
func Start(ctx context.Context, command []string, dir string, environment []string) (*Process, error) {
	if len(command) == 0 || command[0] == "" {
		return nil, fmt.Errorf("MCP command required")
	}
	life, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(life, command[0], command[1:]...)
	cmd.Dir = dir
	cmd.Env = environment
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, errors.Join(err, in.Close())
	}
	// stdout is protocol-only; bounded diagnostics are intentionally not forwarded
	// because plugin messages may contain unrelated session metadata.
	if err = cmd.Start(); err != nil {
		cancel()
		return nil, errors.Join(err, in.Close())
	}
	p := &Process{cmd: cmd, input: in, output: make(chan response), gate: make(chan struct{}, 1), done: make(chan struct{}), cancel: cancel}
	p.gate <- struct{}{}
	go func() {
		defer close(p.output)
		scan := bufio.NewScanner(out)
		scan.Buffer(make([]byte, 4096), 2<<20)
		for scan.Scan() {
			var r response
			if err := json.Unmarshal(scan.Bytes(), &r); err != nil {
				r.failure = fmt.Errorf("invalid MCP JSON: %w", err)
			}
			select {
			case p.output <- r:
			case <-life.Done():
				return
			}
		}
		if err := scan.Err(); err != nil {
			select {
			case p.output <- response{failure: err}:
			case <-life.Done():
			}
		}
	}()
	// StdoutPipe requires Wait after reading completes.
	go func() {
		<-life.Done()
		p.waitErr = cmd.Wait()
		close(p.done)
	}()
	initContext, stopInit := context.WithTimeout(ctx, 10*time.Second)
	defer stopInit()
	init, err := p.call(initContext, "initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "coord-telepathy", "version": "1"}})
	if err != nil {
		return nil, errors.Join(err, p.Close())
	}
	var info struct {
		ProtocolVersion string                         `json:"protocolVersion"`
		ServerInfo      struct{ Name, Version string } `json:"serverInfo"`
	}
	if err = json.Unmarshal(init, &info); err != nil {
		return nil, errors.Join(err, p.Close())
	}
	if info.ServerInfo.Name != "telepathy" || info.ServerInfo.Version != "0.8.0" || info.ProtocolVersion != "2024-11-05" {
		return nil, errors.Join(fmt.Errorf("unsupported MCP server %s %s / %s", info.ServerInfo.Name, info.ServerInfo.Version, info.ProtocolVersion), p.Close())
	}
	if err = p.write(initContext, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		return nil, errors.Join(err, p.Close())
	}
	return p, nil
}

// Close cancels the MCP process and waits for OS resources. Repeated calls are safe.
func (p *Process) Close() error {
	p.once.Do(func() {
		p.selfClosed = true
		p.cancel()
		_ = p.input.Close()
	})
	<-p.done
	if p.waitErr == nil {
		return nil
	}
	// Intentional cancellation kills the owned process, so the signal death is
	// not a failure.
	var exit *exec.ExitError
	if errors.As(p.waitErr, &exit) {
		return nil
	}
	// A server that exits cleanly on EOF stdin before the kill lands is still a
	// successful close, but exec reports the cancellation of CommandContext
	// instead of the exit status: Wait returns ctx.Err() when the context was
	// cancelled and the process finished successfully.
	if p.selfClosed && errors.Is(p.waitErr, context.Canceled) {
		return nil
	}
	// A deadline from the parent context is not our cancellation and stays an
	// error: the caller asked for a session that outlived its budget.
	return p.waitErr
}
func (p *Process) write(ctx context.Context, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { _, err := fmt.Fprintln(p.input, string(b)); done <- err }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		p.cancel()
		_ = p.input.Close()
		<-done
		return ctx.Err()
	case <-p.done:
		return fmt.Errorf("MCP process exited")
	}
}
func (p *Process) call(ctx context.Context, method string, args any) (json.RawMessage, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.gate:
	}
	defer func() { p.gate <- struct{}{} }()
	p.id++
	id := p.id
	if err := p.write(ctx, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": args}); err != nil {
		return nil, err
	}
	for {
		select {
		case <-ctx.Done():
			p.cancel()
			return nil, ctx.Err()
		case r, ok := <-p.output:
			if !ok {
				return nil, fmt.Errorf("MCP stdout closed")
			}
			if r.failure != nil {
				return nil, r.failure
			}
			if r.ID != id {
				continue
			}
			if r.JSONRPC != "2.0" {
				return nil, fmt.Errorf("invalid MCP response version")
			}
			if len(r.Error) > 0 && string(r.Error) != "null" {
				return nil, fmt.Errorf("MCP error: %s", r.Error)
			}
			if len(r.Result) == 0 {
				return nil, fmt.Errorf("MCP result missing")
			}
			return r.Result, nil
		}
	}
}

// Call invokes one text tool with a bounded deadline and propagates isError.
func (p *Process) Call(ctx context.Context, name string, args map[string]any) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := p.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", err
	}
	var result struct {
		IsError bool                          `json:"isError"`
		Content []struct{ Type, Text string } `json:"content"`
	}
	if err = json.Unmarshal(raw, &result); err != nil {
		return "", err
	}
	var parts []string
	for _, c := range result.Content {
		if c.Type == "text" {
			parts = append(parts, c.Text)
		}
	}
	text := strings.Join(parts, "\n")
	if result.IsError {
		return text, fmt.Errorf("telepathy tool %s: %s", name, text)
	}
	return text, nil
}
