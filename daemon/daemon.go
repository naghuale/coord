// Package daemon hosts the coordinator loop and its operational listeners.
// It owns the Coordinator passed to New until Run returns.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	coord "coord"
)

type Config struct {
	ControlAddr     string
	MetricsAddr     string
	ControlToken    string
	ShutdownTimeout time.Duration
	CloseTransport  func() error
}

func DefaultConfig() Config {
	return Config{
		ControlAddr:     "127.0.0.1:8090",
		MetricsAddr:     "127.0.0.1:9091",
		ShutdownTimeout: 10 * time.Second,
	}
}

type Daemon struct {
	coordinator    *coord.Coordinator
	config         Config
	controlLn      net.Listener
	metricsLn      net.Listener
	controlHTTP    *http.Server
	metricsHTTP    *http.Server
	transportReady atomic.Bool
	lifecycleMu    sync.Mutex
	started        bool
	closed         bool
	serverWG       sync.WaitGroup
	closeOnce      sync.Once
	closeErr       error
}

// New binds both listeners before the daemon can become ready. The supplied
// Coordinator already owns and has validated its durable stores.
func New(c *coord.Coordinator, cfg Config) (*Daemon, error) {
	if c == nil || c.Metrics == nil || cfg.ControlAddr == "" || cfg.MetricsAddr == "" || cfg.ControlToken == "" || cfg.ShutdownTimeout <= 0 {
		return nil, fmt.Errorf("coordinator, metrics, listeners, control token and positive shutdown timeout are required")
	}
	if !isLoopbackAddr(cfg.ControlAddr) || !isLoopbackAddr(cfg.MetricsAddr) {
		return nil, fmt.Errorf("control and metrics listeners must use loopback; expose them remotely through an authenticated TLS proxy")
	}
	controlLn, err := net.Listen("tcp", cfg.ControlAddr)
	if err != nil {
		return nil, fmt.Errorf("listen control: %w", err)
	}
	metricsLn, err := net.Listen("tcp", cfg.MetricsAddr)
	if err != nil {
		_ = controlLn.Close()
		return nil, fmt.Errorf("listen metrics: %w", err)
	}
	d := &Daemon{coordinator: c, config: cfg, controlLn: controlLn, metricsLn: metricsLn}
	controlMux := http.NewServeMux()
	controlMux.HandleFunc("/readyz", d.readyHandler)
	controlMux.Handle("/", d.afterTransportReady(c.ControlHandler(cfg.ControlToken)))
	metricsMux := http.NewServeMux()
	metricsMux.HandleFunc("/readyz", d.readyHandler)
	metricsMux.Handle("/", c.Metrics)
	d.controlHTTP = &http.Server{Handler: controlMux, ReadHeaderTimeout: 5 * time.Second}
	d.metricsHTTP = &http.Server{Handler: metricsMux, ReadHeaderTimeout: 5 * time.Second}
	return d, nil
}

func (d *Daemon) ControlAddr() string { return d.controlLn.Addr().String() }
func (d *Daemon) MetricsAddr() string { return d.metricsLn.Addr().String() }

// Close releases listeners and coordinator state when the daemon will not be
// run. Once Run has started, cancel its context and let its graceful shutdown
// release the stores.
func (d *Daemon) Close() error {
	d.lifecycleMu.Lock()
	if d.started && !d.closed {
		d.lifecycleMu.Unlock()
		return fmt.Errorf("daemon has started; cancel its Run context to stop it")
	}
	d.closed = true
	d.lifecycleMu.Unlock()
	return d.close()
}

func (d *Daemon) readyHandler(w http.ResponseWriter, _ *http.Request) {
	if reason := d.notReadyReason(); reason != "" {
		http.Error(w, "not ready: "+reason, http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready\n"))
}

func (d *Daemon) notReadyReason() string {
	if !d.transportReady.Load() {
		return "transport_receive_not_initialized"
	}
	if d.coordinator.Workflow != nil {
		if err := d.coordinator.Workflow.Completion(); err != nil {
			return "orchestration_pending"
		}
	}
	if d.coordinator.RecoveryPending() {
		return "supervisor_recovery_pending"
	}
	return ""
}

// Recovery control remains available after transport initialization while
// /readyz stays closed until external-supervisor recovery is resolved.
func (d *Daemon) afterTransportReady(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !d.transportReady.Load() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Run restores due work before readiness, then owns the coordinator loop and
// listeners until cancellation or a fatal loop/server error. It closes the
// Coordinator after all HTTP handlers and the loop have stopped.
func (d *Daemon) Run(ctx context.Context) (runErr error) {
	d.lifecycleMu.Lock()
	if d.started || d.closed {
		d.lifecycleMu.Unlock()
		return fmt.Errorf("daemon already started")
	}
	d.started = true
	d.lifecycleMu.Unlock()
	defer func() { runErr = errors.Join(runErr, d.close()) }()
	if d.config.CloseTransport != nil {
		defer func() { runErr = errors.Join(runErr, d.config.CloseTransport()) }()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.coordinator.RecoverWorkflow(ctx); err != nil {
		return fmt.Errorf("workflow recovery: %w", err)
	}
	if err := d.coordinator.Tick(ctx); err != nil {
		return fmt.Errorf("startup tick: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	serverErrs := make(chan error, 2)
	d.serverWG.Add(2)
	go d.serve(d.controlHTTP, d.controlLn, serverErrs)
	go d.serve(d.metricsHTTP, d.metricsLn, serverErrs)
	loopErr := make(chan error, 1)
	go func() {
		loopErr <- d.coordinator.RunWithReadiness(runCtx, func() { d.transportReady.Store(true) })
	}()

	var result error
	loopStopped := false
	select {
	case result = <-loopErr:
		loopStopped = true
		if errors.Is(result, context.Canceled) && ctx.Err() != nil {
			result = nil
		}
	case result = <-serverErrs:
		if errors.Is(result, http.ErrServerClosed) {
			result = nil
		}
	case <-ctx.Done():
	}
	d.transportReady.Store(false)
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), d.config.ShutdownTimeout)
	defer shutdownCancel()
	shutdownErr := d.shutdownHTTP(shutdownCtx)
	if !loopStopped {
		select {
		case err := <-loopErr:
			if result == nil && err != nil && !errors.Is(err, context.Canceled) {
				result = err
			}
		case <-shutdownCtx.Done():
			select {
			case err := <-loopErr:
				if result == nil && err != nil && !errors.Is(err, context.Canceled) {
					result = err
				}
			default:
				result = errors.Join(result, fmt.Errorf("coordinator did not stop before shutdown timeout"))
			}
		}
	}
	d.serverWG.Wait()
	for {
		select {
		case err := <-serverErrs:
			result = errors.Join(result, err)
		default:
			return errors.Join(result, shutdownErr)
		}
	}
}

func (d *Daemon) serve(server *http.Server, listener net.Listener, errs chan<- error) {
	defer d.serverWG.Done()
	err := server.Serve(listener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		errs <- err
	}
}

func (d *Daemon) shutdownHTTP(ctx context.Context) error {
	var errs []error
	for _, server := range []*http.Server{d.controlHTTP, d.metricsHTTP} {
		if err := server.Shutdown(ctx); err != nil {
			errs = append(errs, err)
			if closeErr := server.Close(); closeErr != nil {
				errs = append(errs, closeErr)
			}
		}
	}
	return errors.Join(errs...)
}

func (d *Daemon) close() error {
	d.closeOnce.Do(func() {
		_ = d.controlLn.Close()
		_ = d.metricsLn.Close()
		d.closeErr = d.coordinator.Close()
		d.lifecycleMu.Lock()
		d.closed = true
		d.lifecycleMu.Unlock()
	})
	return d.closeErr
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
