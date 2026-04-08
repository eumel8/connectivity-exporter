package main

import (
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
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
func runChecks(targets []string, interval, timeout time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run an initial check immediately on startup.
	for _, t := range targets {
		checkTarget(t, timeout)
	}

	for range ticker.C {
		for _, t := range targets {
			go checkTarget(t, timeout)
		}
	}
}

func main() {
	// --- Configuration via environment variables ---

	// TARGETS: comma-separated list of host:port, e.g. "1.2.3.4:443,example.com:80"
	rawTargets := os.Getenv("TARGETS")
	if rawTargets == "" {
		slog.Error("TARGETS environment variable is not set or empty")
		os.Exit(1)
	}

	targets := []string{}
	for _, t := range strings.Split(rawTargets, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			targets = append(targets, t)
		}
	}
	if len(targets) == 0 {
		slog.Error("no valid targets found in TARGETS")
		os.Exit(1)
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

	slog.Info("connectivity-exporter starting",
		"targets", targets,
		"interval", interval,
		"timeout", timeout,
		"listen_addr", listenAddr,
	)

	// Start background check loop.
	go runChecks(targets, interval, timeout)

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
