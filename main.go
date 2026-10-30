package main

import (
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	defaultInterval   = 60 * time.Second
	defaultListenAddr = ":9090"
	defaultTimeout    = 5 * time.Second
)

var (
	connectivityUp = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "connectivity_up",
			Help: "1 if the TCP connection to the target succeeded, 0 otherwise.",
		},
		[]string{"target"},
	)

	connectivityLatencySeconds = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "connectivity_latency_seconds",
			Help: "TCP dial latency in seconds for each target. Set to -1 when unreachable.",
		},
		[]string{"target"},
	)

	connectivityChecksTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "connectivity_checks_total",
			Help: "Total number of connectivity checks performed per target.",
		},
		[]string{"target", "result"},
	)
)

func init() {
	prometheus.MustRegister(connectivityUp, connectivityLatencySeconds, connectivityChecksTotal)
}

// checkTarget dials the given host:port and updates the Prometheus metrics.
func checkTarget(target string, timeout time.Duration) {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", target, timeout)
	elapsed := time.Since(start)

	if err != nil {
		slog.Warn("connectivity check failed", "target", target, "error", err)
		connectivityUp.WithLabelValues(target).Set(0)
		connectivityLatencySeconds.WithLabelValues(target).Set(-1)
		connectivityChecksTotal.WithLabelValues(target, "failure").Inc()
		return
	}
	conn.Close()

	slog.Debug("connectivity check succeeded", "target", target, "latency", elapsed)
	connectivityUp.WithLabelValues(target).Set(1)
	connectivityLatencySeconds.WithLabelValues(target).Set(elapsed.Seconds())
	connectivityChecksTotal.WithLabelValues(target, "success").Inc()
}

// runChecks continuously checks all targets at the given interval.
func runChecks(targets *targetSet, interval, timeout time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run an initial check immediately on startup.
	targets.check(timeout)

	for range ticker.C {
		targets.check(timeout)
	}
}

// targetSet serializes replacements with checks so an in-flight check cannot
// recreate metrics belonging to a removed target.
type targetSet struct {
	mu      sync.RWMutex
	targets []string
}

func (s *targetSet) check(timeout time.Duration) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var checks sync.WaitGroup
	for _, target := range s.targets {
		checks.Add(1)
		go func() {
			defer checks.Done()
			checkTarget(target, timeout)
		}()
	}
	checks.Wait()
}

func (s *targetSet) replace(targets []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := make(map[string]bool, len(targets))
	for _, target := range targets {
		active[target] = true
	}
	for _, target := range s.targets {
		if !active[target] {
			connectivityUp.DeleteLabelValues(target)
			connectivityLatencySeconds.DeleteLabelValues(target)
			connectivityChecksTotal.DeleteLabelValues(target, "success")
			connectivityChecksTotal.DeleteLabelValues(target, "failure")
		}
	}
	s.targets = targets
}

// parseTargets validates a comma- or newline-separated list of host:port targets.
func parseTargets(rawTargets string) []string {
	targets := []string{}
	seen := make(map[string]bool)
	for _, t := range strings.Split(strings.ReplaceAll(rawTargets, "\n", ","), ",") {
		// Strip surrounding whitespace and any surrounding quotes that shells
		// or Kubernetes YAML may introduce around IPv6 addresses, e.g.
		//   "[2001:db8::1]:443"  →  [2001:db8::1]:443
		t = strings.TrimSpace(t)
		t = strings.Trim(t, `"`)
		if t == "" {
			continue
		}
		// Validate the address is a parseable host:port before accepting it.
		if _, err := net.ResolveTCPAddr("tcp", t); err != nil {
			slog.Error("invalid target, skipping", "target", t, "error", err)
			continue
		}
		if !seen[t] {
			targets = append(targets, t)
			seen[t] = true
		}
	}
	return targets
}

func main() {
	// --- Configuration via environment variables ---

	// TARGETS_FILE: optional downloaded targets, one per line or comma-separated.
	// Fall back to TARGETS if the file is unreadable, empty, or has no valid targets.
	var targets []string
	if path := os.Getenv("TARGETS_FILE"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("cannot read TARGETS_FILE, falling back to TARGETS", "path", path, "error", err)
		} else {
			targets = parseTargets(strings.ReplaceAll(string(data), "\n", ","))
			if len(targets) == 0 {
				slog.Warn("no valid targets in TARGETS_FILE, falling back to TARGETS", "path", path)
			}
		}
	}
	if len(targets) == 0 {
		// TARGETS: comma-separated list of host:port, e.g. "1.2.3.4:443,example.com:80"
		targets = parseTargets(os.Getenv("TARGETS"))
	}

	// INTERVAL: check interval in seconds (default: 60)
	interval := defaultInterval
	if v := os.Getenv("INTERVAL"); v != "" {
		secs, err := strconv.Atoi(v)
		if err != nil || secs <= 0 {
			slog.Warn("invalid INTERVAL value, using default", "value", v, "default", defaultInterval)
		} else {
			interval = time.Duration(secs) * time.Second
		}
	}

	// TIMEOUT: per-check dial timeout in seconds (default: 5)
	timeout := defaultTimeout
	if v := os.Getenv("TIMEOUT"); v != "" {
		secs, err := strconv.Atoi(v)
		if err != nil || secs <= 0 {
			slog.Warn("invalid TIMEOUT value, using default", "value", v, "default", defaultTimeout)
		} else {
			timeout = time.Duration(secs) * time.Second
		}
	}

	// LISTEN_ADDR: address for the metrics HTTP server (default: :9090)
	listenAddr := defaultListenAddr
	if v := os.Getenv("LISTEN_ADDR"); v != "" {
		listenAddr = v
	}

	// LOG_LEVEL: debug | info | warn | error (default: info)
	logLevel := slog.LevelInfo
	if v := strings.ToLower(os.Getenv("LOG_LEVEL")); v == "debug" {
		logLevel = slog.LevelDebug
	} else if v == "warn" {
		logLevel = slog.LevelWarn
	} else if v == "error" {
		logLevel = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})))

	pollInterval := 300 * time.Second
	if v := os.Getenv("TARGETS_POLL_INTERVAL"); v != "" {
		secs, err := strconv.Atoi(v)
		if err != nil || secs <= 0 || secs > 86400 {
			slog.Warn("invalid TARGETS_POLL_INTERVAL, using default", "value", v)
		} else {
			pollInterval = time.Duration(secs) * time.Second
		}
	}
	source := targetSource{url: os.Getenv("TARGETS_URL"), client: &http.Client{Timeout: 15 * time.Second}}
	if source.url != "" {
		if downloaded, err := source.fetch(); err != nil {
			slog.Warn("target download failed, using startup fallback", "error", err)
		} else {
			targets = downloaded
		}
	}
	if len(targets) == 0 {
		slog.Error("no valid targets found in TARGETS_URL, TARGETS_FILE or TARGETS")
		os.Exit(1)
	}
	activeTargets := &targetSet{targets: targets}
	if source.url != "" {
		go source.poll(activeTargets, pollInterval)
	}

	slog.Info("connectivity-exporter starting",
		"targets", targets,
		"interval", interval,
		"timeout", timeout,
		"listen_addr", listenAddr,
	)

	// Start background check loop.
	go runChecks(activeTargets, interval, timeout)

	// Expose /metrics and a minimal /healthz endpoint.
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	slog.Info("HTTP server listening", "addr", listenAddr)
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		slog.Error("HTTP server failed", "error", err)
		os.Exit(1)
	}
}
