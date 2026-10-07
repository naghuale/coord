//go:build unix

package telepathyhost

import (
	"context"
	"coord"
	"coord/checkpoint"
	"coord/fsm"
	"coord/internal/atomicfile"
	"coord/store"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

// Run owns a single standalone host or command worker until receipt or cancellation.
// Snapshots are exclusive. An interrupted command is never rerun automatically.
func Run(ctx context.Context, cfg Config) (result fsm.Run, err error) {
	if err = cfg.validate(); err != nil {
		return result, err
	}
	releaseSession, lockErr := (checkpoint.File{Path: filepath.Join(cfg.Home, "coord-sessions", cfg.Session)}).Lock()
	if lockErr != nil {
		return result, fmt.Errorf("acquire Telepathy session: %w", lockErr)
	}
	defer func() { err = errors.Join(err, releaseSession()) }()
	p, client, tp, err := connect(ctx, cfg)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, p.Close()) }()
	if cfg.Role == "worker" {
		return worker(ctx, cfg, tp)
	}
	canonical, err := store.New(checkpoint.File{Path: filepath.Join(cfg.State, "runs.json")})
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, canonical.Close()) }()
	runs := canonical.All()
	if len(runs) > 1 {
		return result, fmt.Errorf("standalone host owns exactly one run")
	}
	options := coord.DefaultConfig()
	// Telepathy limits sends to 20 / recipient / 10m. Backoff exceeds its
	// two-minute exact-message suppression and avoids probe storms.
	options.AckTimeout = 3 * time.Minute
	options.ProbeTimeout = 130 * time.Second
	options.SendTimeout = 12 * time.Second
	options.Retry.BaseDelay = 130 * time.Second
	options.Retry.MaxDelay = 5 * time.Minute
	options.Retry.Jitter = false
	coordinator, err := coord.New(tp, canonical, options)
	if err != nil {
		return result, err
	}
	meta := filepath.Join(cfg.State, "binding.json")
	binding, _ := json.Marshal(struct {
		Session string
		Task    fsm.Run
	}{cfg.Session, cfg.Task})
	old, loadErr := readBinding(meta)
	if loadErr != nil {
		return result, loadErr
	}
	if len(old) > 0 && string(old) != string(binding) {
		return result, fmt.Errorf("host identity/task binding changed; fenced migration required")
	}
	if len(old) == 0 {
		if len(runs) > 0 {
			return result, fmt.Errorf("canonical state lacks identity binding")
		}
		if err = atomicfile.Write(meta, binding); err != nil {
			return result, err
		}
	}
	existing, ok := canonical.Get(cfg.Task.RunID)
	if !ok {
		if len(runs) != 0 {
			return result, fmt.Errorf("canonical run differs from configuration")
		}
		// Assign may fail delivery after durable commit; the loop retries its outbox.
		if assignErr := coordinator.Assign(ctx, cfg.Task); assignErr != nil {
			if _, saved := canonical.Get(cfg.Task.RunID); !saved {
				return result, assignErr
			}
		}
	} else {
		result = existing
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- coordinator.Run(child) }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case loopErr := <-done:
			return result, errors.Join(loopErr, client.Err())
		case <-ticker.C:
			result, _ = canonical.Get(cfg.Task.RunID)
			// The receipt must leave the durable outbox before this one-shot host stops.
			// Delivery is still not executor acceptance; a restart handles RESULT replay.
			if cfg.ExitOnTerminal && result.Terminal() && len(result.Outbox) == 0 {
				cancel()
				<-done
				return result, nil
			}
		}
	}
}
