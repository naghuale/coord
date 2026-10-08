// coordd is the production process owner for the current Run coordinator.
package main

import (
	"context"
	"coord"
	"coord/checkpoint"
	"coord/daemon"
	"coord/internal/coorddconfig"
	"coord/internal/filelock"
	"coord/roles"
	"coord/store"
	"coord/telepathy"
	"coord/transport"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type transportFactory func(context.Context, coorddconfig.Config) (transport.Transport, func() error, error)
type storeFactory func(string) (store.RunStore, error)
type roleRegistryFactory func(string) (*roles.Registry, error)

func main() { os.Exit(mainRun(os.Args[1:], productionTransport)) }

func mainRun(args []string, factory transportFactory) int {
	return mainRunWithWriter(args, factory, os.Stderr)
}

func mainRunWithWriter(args []string, factory transportFactory, stderr io.Writer) int {
	if len(args) != 2 || args[0] != "-config" || args[1] == "" {
		writeStartupFailure(stderr, "config", "usage: coordd -config <path>", "unset", "not acquired", "not acquired")
		return 2
	}
	cfg, err := coorddconfig.Load(args[1])
	if err != nil {
		section, reason, ok := coorddconfig.FailureDetails(err)
		if !ok {
			section, reason = "config", err.Error()
		}
		kind := cfg.Transport.Kind
		if kind == "" {
			kind = "unset"
		}
		writeStartupFailure(stderr, section, reason, kind, "not acquired", "not acquired")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err = runWithWriter(ctx, cfg, factory, stderr); err != nil {
		var startErr *startupFailure
		if errors.As(err, &startErr) {
			writeStartupFailure(stderr, startErr.section, startErr.err.Error(), cfg.Transport.Kind, startErr.ownership, startErr.namespaceLock)
			return startErr.exitCode
		}
		if section, reason, ok := coorddconfig.FailureDetails(err); ok {
			writeStartupFailure(stderr, section, reason, cfg.Transport.Kind, "not acquired", "not acquired")
			return 2
		}
		writeStartupFailure(stderr, "runtime", err.Error(), cfg.Transport.Kind, "released", "released")
		return 1
	}
	return 0
}

type startupFailure struct {
	section       string
	ownership     string
	namespaceLock string
	exitCode      int
	err           error
}

func (e *startupFailure) Error() string { return e.err.Error() }
func (e *startupFailure) Unwrap() error { return e.err }

func writeStartupFailure(w io.Writer, section, reason, kind, ownership, namespaceLock string) {
	reason = strings.Join(strings.Fields(reason), " ")
	kind = strings.Join(strings.Fields(kind), " ")
	_, _ = fmt.Fprintln(w, "coordd: not started")
	_, _ = fmt.Fprintf(w, "reason: %s: %s\n", section, reason)
	_, _ = fmt.Fprintf(w, "transport: %s\n", kind)
	_, _ = fmt.Fprintln(w, "supported transports: telepathy")
	if ownership != "" {
		_, _ = fmt.Fprintf(w, "ownership: %s\n", ownership)
	}
	if namespaceLock != "" {
		_, _ = fmt.Fprintf(w, "namespace lock: %s\n", namespaceLock)
	}
}

func run(ctx context.Context, cfg coorddconfig.Config, factory transportFactory) (err error) {
	return runWithWriter(ctx, cfg, factory, os.Stderr)
}

func runWithWriter(ctx context.Context, cfg coorddconfig.Config, factory transportFactory, stderr io.Writer) (err error) {
	return runWithWriterFactories(ctx, cfg, factory,
		func(path string) (store.RunStore, error) {
			return store.NewCanonical(checkpoint.CanonicalFile{Path: path})
		},
		roles.New,
		stderr,
	)
}

func runWithFactories(ctx context.Context, cfg coorddconfig.Config, factory transportFactory, openStore storeFactory, openRoles roleRegistryFactory) error {
	return runWithWriterFactories(ctx, cfg, factory, openStore, openRoles, io.Discard)
}

func runWithWriterFactories(ctx context.Context, cfg coorddconfig.Config, factory transportFactory, openStore storeFactory, openRoles roleRegistryFactory, stderr io.Writer) (err error) {
	if err = cfg.Validate(); err != nil {
		return err
	}
	if factory == nil {
		return &coorddconfig.InvalidError{Section: "transport", Reason: "production agent transport factory required"}
	}
	releaseNamespace, err := filelock.Lock(filepath.Join(cfg.StateDir, "namespace.lock"))
	if err != nil {
		return &startupFailure{section: "runtime", ownership: "not acquired", namespaceLock: "not acquired", exitCode: 1, err: fmt.Errorf("acquire state namespace: %w", err)}
	}
	defer func() { err = errors.Join(err, releaseNamespace()) }()

	legacyPath := filepath.Join(cfg.StateDir, "runs.json")
	releaseLegacy, err := (checkpoint.File{Path: legacyPath}).Lock()
	if errors.Is(err, filelock.ErrLocked) {
		return runDiagnostic(ctx, cfg, stderr, "legacy writer owns namespace")
	}
	if err != nil {
		return &startupFailure{section: "runtime", ownership: "released", namespaceLock: "released", exitCode: 1, err: fmt.Errorf("acquire legacy compatibility lock: %w", err)}
	}
	defer func() { err = errors.Join(err, releaseLegacy()) }()

	legacyRuns, legacyErr := (checkpoint.File{Path: legacyPath}).Load()
	if legacyErr != nil {
		return runDiagnostic(ctx, cfg, stderr, "legacy snapshot is invalid: "+legacyErr.Error())
	}
	for id, run := range legacyRuns {
		if !run.Terminal() {
			return runDiagnostic(ctx, cfg, stderr, fmt.Sprintf("active legacy run %s requires explicit recovery", id))
		}
	}

	canonical, err := openStore(filepath.Join(cfg.StateDir, "canonical.json"))
	if errors.Is(err, filelock.ErrUnavailable) {
		return &startupFailure{section: "runtime", ownership: "released", namespaceLock: "released", exitCode: 1, err: fmt.Errorf("open canonical store: %w", err)}
	}
	if err != nil {
		return runDiagnostic(ctx, cfg, stderr, "canonical state is not ready: "+err.Error())
	}
	registry, err := openRoles(filepath.Join(cfg.StateDir, "roles.json"))
	if err != nil {
		return &startupFailure{section: "runtime", ownership: "released", namespaceLock: "released", exitCode: 1, err: errors.Join(fmt.Errorf("open role registry: %w", err), canonical.Close())}
	}

	tp, closeTransport, err := factory(ctx, cfg)
	if err != nil {
		var closeErr error
		if closeTransport != nil {
			closeErr = closeTransport()
		}
		return &startupFailure{section: "transport", ownership: "released", namespaceLock: "released", exitCode: 1, err: errors.Join(fmt.Errorf("start required transport: %w", err), closeErr, registry.Close(), canonical.Close())}
	}
	if tp == nil || closeTransport == nil {
		var closeErr error
		if closeTransport != nil {
			closeErr = closeTransport()
		}
		return &startupFailure{section: "transport", ownership: "released", namespaceLock: "released", exitCode: 1, err: errors.Join(fmt.Errorf("transport factory returned incomplete runtime"), closeErr, registry.Close(), canonical.Close())}
	}

	coordinator, err := coord.New(tp, canonical, coord.DefaultConfig())
	if err != nil {
		return &startupFailure{section: "runtime", ownership: "released", namespaceLock: "released", exitCode: 1, err: errors.Join(fmt.Errorf("create coordinator: %w", err), closeTransport(), registry.Close(), canonical.Close())}
	}
	coordinator.RoleRegistry = registry
	host, err := daemon.New(coordinator, daemon.Config{
		ControlAddr: cfg.ControlAddr, MetricsAddr: cfg.MetricsAddr,
		ControlToken: cfg.ControlToken, ShutdownTimeout: 10 * time.Second,
		CloseTransport: closeTransport,
	})
	if err != nil {
		return &startupFailure{section: "runtime", ownership: "released", namespaceLock: "released", exitCode: 1, err: errors.Join(fmt.Errorf("create daemon host: %w", err), closeTransport(), coordinator.Close())}
	}
	// Daemon.Run owns shutdown ordering: HTTP handlers and loop, transport,
	// registry/store. The namespace lock remains held until Run has returned.
	if err = host.Run(ctx); err != nil {
		return &startupFailure{section: "runtime", ownership: "released", namespaceLock: "released", exitCode: 1, err: err}
	}
	return nil
}

func runDiagnostic(ctx context.Context, cfg coorddconfig.Config, stderr io.Writer, reason string) error {
	reason = strings.Join(strings.Fields(reason), " ")
	_, _ = fmt.Fprintf(stderr, "coordd: not ready: %s\n", reason)
	return daemon.RunDiagnostic(ctx, cfg.ControlAddr, cfg.MetricsAddr, reason, 10*time.Second)
}

func productionTransport(ctx context.Context, cfg coorddconfig.Config) (transport.Transport, func() error, error) {
	environment := make([]string, 0, len(os.Environ())+2)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "TELEPATHY_HOME=") && !strings.HasPrefix(value, "TELEPATHY_AGENT_PID=") {
			environment = append(environment, value)
		}
	}
	environment = append(environment,
		"TELEPATHY_HOME="+cfg.Transport.Home,
		"TELEPATHY_AGENT_PID="+strconv.Itoa(cfg.Transport.AnchorPID),
	)
	peers := make(map[string]ed25519.PublicKey, len(cfg.PeerKeys))
	for session, key := range cfg.PeerKeys {
		peers[session] = ed25519.PublicKey(key)
	}
	return telepathy.OpenTransport(ctx, telepathy.TransportConfig{
		Command:   []string{"node", cfg.Transport.Server, "--agent", "opencode"},
		Directory: cfg.Transport.Directory, Environment: environment,
		Session: cfg.Transport.Session, PollInterval: time.Second,
		PrivateKey: ed25519.PrivateKey(cfg.PrivateKey), PeerKeys: peers,
	})
}
