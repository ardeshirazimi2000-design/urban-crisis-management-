package httpx

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Minimal Prometheus text-format registry (no external dependency):
// request counters/latency histograms per route plus pluggable gauges
// (outbox backlog, oldest unpublished event age, notification states...).

var latencyBuckets = []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

type routeStats struct {
	count   map[int]uint64
	buckets []uint64
	sum     float64
	total   uint64
}

type GaugeFunc func(ctx context.Context) (map[string]float64, error)

type registry struct {
	mu     sync.Mutex
	routes map[string]*routeStats
	gauges map[string]GaugeFunc
	help   map[string]string
}

var Metrics = &registry{routes: map[string]*routeStats{}, gauges: map[string]GaugeFunc{}, help: map[string]string{}}

func (m *registry) Observe(route string, status int, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rs, ok := m.routes[route]
	if !ok {
		rs = &routeStats{count: map[int]uint64{}, buckets: make([]uint64, len(latencyBuckets))}
		m.routes[route] = rs
	}
	rs.count[status]++
	sec := d.Seconds()
	for i, b := range latencyBuckets {
		if sec <= b {
			rs.buckets[i]++
		}
	}
	rs.sum += sec
	rs.total++
}

// RegisterGauge adds a gauge family; fn returns label-string -> value (label "" for unlabelled).
func (m *registry) RegisterGauge(name, help string, fn GaugeFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gauges[name] = fn
	m.help[name] = help
}

func (m *registry) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		m.mu.Lock()
		routes := make([]string, 0, len(m.routes))
		for k := range m.routes {
			routes = append(routes, k)
		}
		sort.Strings(routes)
		b.WriteString("# TYPE http_requests_total counter\n")
		for _, rt := range routes {
			rs := m.routes[rt]
			for st, c := range rs.count {
				fmt.Fprintf(&b, "http_requests_total{route=%q,status=\"%d\"} %d\n", rt, st, c)
			}
		}
		b.WriteString("# TYPE http_request_duration_seconds histogram\n")
		for _, rt := range routes {
			rs := m.routes[rt]
			for i, le := range latencyBuckets {
				fmt.Fprintf(&b, "http_request_duration_seconds_bucket{route=%q,le=\"%g\"} %d\n", rt, le, rs.buckets[i])
			}
			fmt.Fprintf(&b, "http_request_duration_seconds_bucket{route=%q,le=\"+Inf\"} %d\n", rt, rs.total)
			fmt.Fprintf(&b, "http_request_duration_seconds_sum{route=%q} %g\n", rt, rs.sum)
			fmt.Fprintf(&b, "http_request_duration_seconds_count{route=%q} %d\n", rt, rs.total)
		}
		gauges := make(map[string]GaugeFunc, len(m.gauges))
		for k, v := range m.gauges {
			gauges[k] = v
		}
		help := m.help
		m.mu.Unlock()

		names := make([]string, 0, len(gauges))
		for k := range gauges {
			names = append(names, k)
		}
		sort.Strings(names)
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		for _, name := range names {
			vals, err := gauges[name](ctx)
			if err != nil {
				continue
			}
			fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n", name, help[name], name)
			keys := make([]string, 0, len(vals))
			for k := range vals {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if k == "" {
					fmt.Fprintf(&b, "%s %g\n", name, vals[k])
				} else {
					fmt.Fprintf(&b, "%s{%s} %g\n", name, k, vals[k])
				}
			}
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(b.String()))
	}
}
