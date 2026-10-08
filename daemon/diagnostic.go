package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// RunDiagnostic serves only readiness and startup metrics when durable state
// cannot safely enter the normal runtime. It owns no Coordinator or transport.
func RunDiagnostic(ctx context.Context, controlAddr, metricsAddr, reason string, shutdownTimeout time.Duration) error {
	if ctx == nil || reason == "" || shutdownTimeout <= 0 {
		return fmt.Errorf("context, diagnostic reason and positive shutdown timeout are required")
	}
	if !isLoopbackAddr(controlAddr) || !isLoopbackAddr(metricsAddr) {
		return fmt.Errorf("diagnostic listeners must use loopback")
	}
	controlLn, err := net.Listen("tcp", controlAddr)
	if err != nil {
		return fmt.Errorf("listen diagnostic control: %w", err)
	}
	metricsLn, err := net.Listen("tcp", metricsAddr)
	if err != nil {
		_ = controlLn.Close()
		return fmt.Errorf("listen diagnostic metrics: %w", err)
	}

	ready := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not ready: "+reason, http.StatusServiceUnavailable)
	})
	controlMux := http.NewServeMux()
	controlMux.Handle("/readyz", ready)
	controlMux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	})
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/readyz", ready)
	metricsMux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte("coordd_startup_not_ready 1\n"))
	})
	servers := []*http.Server{
		{Handler: controlMux, ReadHeaderTimeout: 5 * time.Second},
		{Handler: metricsMux, ReadHeaderTimeout: 5 * time.Second},
	}
	listeners := []net.Listener{controlLn, metricsLn}
	serveErr := make(chan error, len(servers))
	for i := range servers {
		go func(server *http.Server, listener net.Listener) {
			if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serveErr <- err
				return
			}
			serveErr <- nil
		}(servers[i], listeners[i])
	}

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		if err != nil {
			_ = servers[0].Close()
			_ = servers[1].Close()
			return fmt.Errorf("diagnostic listener failed: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	var errs []error
	for _, server := range servers {
		if err := server.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, err, server.Close())
		}
	}
	return errors.Join(errs...)
}
