//go:build unix

package rooms

import (
	"context"
	"coord/internal/atomicfile"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type exportIntent struct {
	ID         string
	Revision   uint64
	Content    bool
	Projection json.RawMessage
}
type historyState struct {
	Cursor         uint64
	Commit         string
	IncludeContent bool
	Error          string
	Pending        *exportIntent
}

// HistoryStatus reports durable export backlog separately from task progress.
type HistoryStatus struct {
	Path           string `json:"path"`
	Cursor         uint64 `json:"cursor"`
	Revision       uint64 `json:"revision"`
	Backlog        uint64 `json:"backlog"`
	Commit         string `json:"commit"`
	IncludeContent bool   `json:"include_content"`
	Error          string `json:"error,omitempty"`
	Pending        string `json:"pending,omitempty"`
}
type historyRoom struct {
	ID      string   `json:"id"`
	Task    string   `json:"task"`
	Title   string   `json:"title"`
	Closed  bool     `json:"closed"`
	Members []Member `json:"members"`
	Events  []Event  `json:"events"`
}
type projection struct {
	Version        int           `json:"version"`
	Revision       uint64        `json:"revision"`
	IncludeContent bool          `json:"include_content"`
	Rooms          []historyRoom `json:"rooms"`
}

func project(d snapshot, content bool) ([]byte, error) {
	p := projection{Version: 1, Revision: d.Revision, IncludeContent: content, Rooms: []historyRoom{}}
	for _, r := range d.Rooms {
		v := historyRoom{ID: r.ID, Task: r.Task, Title: r.Title, Closed: r.Closed, Members: []Member{}, Events: append([]Event{}, r.Events...)}
		for _, member := range r.Members {
			s := cloneMember(member.Member)
			s.IndependentAudit = independentAudit(&d, r.ID, s)
			// Runtime routes and native connection identifiers do not belong in Git.
			s.Ref = ""
			s.Address = ""
			s.ConnectionID = ""
			if !content {
				s.Readiness.Capabilities = nil
				s.Readiness.Blockers = nil
				s.Instructions = ""
				s.Checkpoint.Progress = ""
				s.Checkpoint.ArtifactRefs = nil
				for i := range s.Checkpoint.PendingActions {
					s.Checkpoint.PendingActions[i].Description = ""
				}
			}
			v.Members = append(v.Members, s)
		}
		sort.Slice(v.Members, func(i, j int) bool { return v.Members[i].SessionID < v.Members[j].SessionID })
		if !content {
			v.Title = ""
			v.Task = ""
			for i := range v.Events {
				v.Events[i].Text = ""
			}
		}
		p.Rooms = append(p.Rooms, v)
	}
	sort.Slice(p.Rooms, func(i, j int) bool { return p.Rooms[i].ID < p.Rooms[j].ID })
	return json.MarshalIndent(p, "", "  ")
}

// HistoryStatus never includes capabilities or pending export contents.
func (m *Manager) HistoryStatus() HistoryStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.data.History
	status := HistoryStatus{Path: filepath.Join(m.cfg.State, "history"), Cursor: h.Cursor, Revision: m.data.Revision, Backlog: m.data.Revision - h.Cursor, Commit: h.Commit, IncludeContent: h.IncludeContent, Error: h.Error}
	if h.Pending != nil {
		status.Pending = h.Pending.ID
	}
	return status
}
func gitCommand(ctx context.Context, dir string, args ...string) (string, error) {
	flags := []string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "-c", "user.name=Coord History", "-c", "user.email=coord@localhost"}
	cmd := exec.CommandContext(ctx, "git", append(flags, args...)...)
	cmd.Dir = dir
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "GIT_") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("history git %s: %w: %s", args[0], err, strings.TrimSpace(string(raw)))
	}
	return strings.TrimSpace(string(raw)), nil
}
func prepareHistory(ctx context.Context, path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err = os.Mkdir(path, 0700); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("history must be a real private directory")
	}
	marker := filepath.Join(path, ".coord-history")
	if _, err = os.Lstat(marker); errors.Is(err, os.ErrNotExist) {
		entries, e := os.ReadDir(path)
		if e != nil {
			return e
		}
		if len(entries) != 0 {
			return fmt.Errorf("refuse adopting an existing history directory")
		}
		if err = atomicfile.Write(marker, []byte("Coord history projection v1\n")); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if info, err = os.Lstat(filepath.Join(path, ".git")); errors.Is(err, os.ErrNotExist) {
		if _, err = gitCommand(ctx, path, "init", "--initial-branch=main"); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if !info.IsDir() {
		return fmt.Errorf("history .git must be a directory")
	}
	return nil
}

// Export writes a sanitized Git projection. Its durable intent makes a crash
// after commit but before cursor advancement recoverable without another commit.
func (m *Manager) Export(ctx context.Context, includeContent bool) error {
	m.historyMu.Lock()
	defer m.historyMu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return fmt.Errorf("room manager closed")
	}
	h := m.data.History
	if h.Pending == nil && h.Cursor == m.data.Revision && h.IncludeContent == includeContent && h.Commit != "" {
		m.mu.Unlock()
		return nil
	}
	if m.data.Revision == 0 {
		m.mu.Unlock()
		return nil
	}
	err := m.updateWithRevision(func(d *snapshot) error {
		if d.History.Pending != nil {
			return nil
		}
		raw, err := project(*d, includeContent)
		if err != nil {
			return err
		}
		d.History.Pending = &exportIntent{ID: fingerprint(string(raw)), Revision: d.Revision, Content: includeContent, Projection: raw}
		return nil
	}, false)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	intent := *m.data.History.Pending
	m.mu.Unlock()
	call, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	path := filepath.Join(m.cfg.State, "history")
	commit, exportErr := exportGit(call, path, intent)
	m.mu.Lock()
	defer m.mu.Unlock()
	persistErr := m.updateWithRevision(func(d *snapshot) error {
		if exportErr != nil {
			d.History.Error = exportErr.Error()
			return nil
		}
		if d.History.Pending == nil || d.History.Pending.ID != intent.ID {
			return fmt.Errorf("export intent changed")
		}
		d.History.Cursor = intent.Revision
		d.History.Commit = commit
		d.History.IncludeContent = intent.Content
		d.History.Error = ""
		d.History.Pending = nil
		return nil
	}, false)
	return errors.Join(exportErr, persistErr)
}
func exportGit(ctx context.Context, path string, intent exportIntent) (string, error) {
	if err := prepareHistory(ctx, path); err != nil {
		return "", err
	}
	// Only the latest commit can belong to this single-writer intent.
	message, err := gitCommand(ctx, path, "log", "-1", "--format=%B")
	if err == nil && strings.Contains("\n"+message+"\n", "\nExport-ID: "+intent.ID+"\n") {
		return gitCommand(ctx, path, "rev-parse", "HEAD")
	}
	if err = atomicfile.Write(filepath.Join(path, "projection.json"), intent.Projection); err != nil {
		return "", err
	}
	if _, err = gitCommand(ctx, path, "add", "--", "projection.json", ".coord-history"); err != nil {
		return "", err
	}
	message = fmt.Sprintf("checkpoint(coord): export revision %d\n\nExport-ID: %s\nRevision: %d\n", intent.Revision, intent.ID, intent.Revision)
	if _, err = gitCommand(ctx, path, "commit", "--allow-empty", "-m", message); err != nil {
		return "", err
	}
	return gitCommand(ctx, path, "rev-parse", "HEAD")
}
