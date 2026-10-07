package coord

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
)

type histogram struct {
	Buckets [7]uint64
	Count   uint64
	Sum     float64
}

func (h *histogram) observe(seconds float64) {
	if seconds < 0 {
		seconds = 0
	}
	for i, b := range []float64{.1, .5, 1, 2, 5, 10, 30} {
		if seconds <= b {
			h.Buckets[i]++
		}
	}
	h.Count++
	h.Sum += seconds
}
func (h histogram) write(w http.ResponseWriter, name string) error {
	if _, err := fmt.Fprintf(w, "# TYPE coord_%s histogram\n", name); err != nil {
		return err
	}
	for i, b := range []float64{.1, .5, 1, 2, 5, 10, 30} {
		if _, err := fmt.Fprintf(w, "coord_%s_bucket{le=\"%g\"} %d\n", name, b, h.Buckets[i]); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "coord_%s_bucket{le=\"+Inf\"} %d\ncoord_%s_count %d\ncoord_%s_sum %g\n", name, h.Count, name, h.Count, name, h.Sum)
	return err
}

// Metrics exposes Prometheus text without dependencies or task/session labels.
type Metrics struct {
	mu            sync.Mutex
	counters      map[string]uint64
	ack, delivery histogram
}

func NewMetrics() *Metrics {
	m := &Metrics{counters: map[string]uint64{}}
	for _, name := range []string{"heartbeat_received_total", "heartbeat_miss_total", "stall_total", "split_brain_events_total", "stale_fence_rejections_total", "invalid_fence_rejections_total", "watchdog_restarts_total", "fsm_errors_total", "malformed_messages_total", "send_errors_total", "ack_timeouts_total", "duplicate_messages_total", "rejected_messages_total", "role_updates_total"} {
		m.counters[name] = 0
	}
	return m
}
func (m *Metrics) Inc(name string) { m.mu.Lock(); defer m.mu.Unlock(); m.counters[name]++ }
func (m *Metrics) AckLatency(seconds float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ack.observe(seconds)
}
func (m *Metrics) DeliveryLatency(seconds float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delivery.observe(seconds)
}
func (m *Metrics) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	keys := make([]string, 0, len(m.counters))
	for k := range m.counters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, err := fmt.Fprintf(w, "coord_%s %d\n", k, m.counters[k]); err != nil {
			return
		}
	}
	if err := m.ack.write(w, "ack_latency_seconds"); err != nil {
		return
	}
	_ = m.delivery.write(w, "delivery_latency_seconds")
}
