# connectivity-exporter

A lightweight [Prometheus](https://prometheus.io/) exporter written in Go that
continuously tests TCP connectivity to a configurable list of hosts or IP
addresses and exposes the results as Prometheus metrics.

## What it does

The exporter dials a list of `host:port` (or `ip:port`) targets over TCP at a
regular interval. For every target it records:

- whether the connection succeeded or failed
- how long the TCP handshake took (latency)
- a running total of successful and failed checks

This makes it straightforward to alert on unreachable endpoints, track
connectivity degradation over time, and build dashboards that show the
reachability of external services, internal APIs, or infrastructure nodes from
within a Kubernetes cluster.

### Exposed metrics

| Metric | Type | Description |
|---|---|---|
| `connectivity_up{target}` | Gauge | `1` = reachable, `0` = unreachable |
| `connectivity_latency_seconds{target}` | Gauge | TCP dial latency in seconds; `-1` when unreachable |
| `connectivity_checks_total{target, result}` | Counter | Total checks performed, labelled `success` or `failure` |

Example output at `/metrics`:

```
connectivity_up{target="example.com:80"} 1
connectivity_up{target="10.0.0.1:443"} 0
connectivity_latency_seconds{target="example.com:80"} 0.003821
connectivity_latency_seconds{target="10.0.0.1:443"} -1
connectivity_checks_total{target="example.com:80",result="success"} 42
connectivity_checks_total{target="10.0.0.1:443",result="failure"} 42
```

## Configuration

All configuration is done through environment variables.

| Variable | Required | Default | Description |
|---|---|---|---|
| `TARGETS` | yes | — | Comma-separated list of `host:port` targets, e.g. `1.2.3.4:443,example.com:80` |
| `INTERVAL` | no | `60` | Check interval in seconds |
| `TIMEOUT` | no | `5` | TCP dial timeout per target in seconds |
| `LISTEN_ADDR` | no | `:9090` | Address the metrics HTTP server binds to |
| `LOG_LEVEL` | no | `info` | Log verbosity: `debug`, `info`, `warn`, or `error` |

IPv6 addresses must be enclosed in square brackets, e.g.
`[2001:db8::1]:443`.

## Endpoints

| Path | Description |
|---|---|
| `/metrics` | Prometheus metrics |
| `/healthz` | Health check — returns `200 ok` |

## Build

The project requires **Go 1.25** or later.

```bash
go build -o connectivity-exporter .
```

## Container image

The image is built and published automatically via GitHub Actions on every push
to `main` and on version tags (`v*`).

```
ghcr.io/eumel8/connectivity-exporter:main
ghcr.io/eumel8/connectivity-exporter:1.0.0
```

The Dockerfile uses a two-stage build:

1. **Builder** — `golang:1.25-alpine` compiles a statically linked binary.
2. **Final** — `scratch` base image; contains only the binary, running as UID
   `65534` (nobody).

## Kubernetes deployment

### Deploy

Edit the `TARGETS` value in `deployment.yaml` to match your endpoints, then
apply:

```bash
kubectl apply -f deployment.yaml
```

This creates:

| Resource | Description |
|---|---|
| `Namespace` | `connectivity-exporter` |
| `Deployment` | Single replica, non-root, read-only filesystem, resource limits |
| `Service` | Exposes port `9090` inside the cluster |
| `ServiceMonitor` | For [Prometheus Operator](https://github.com/prometheus-operator/prometheus-operator) users |

The Pod template carries standard scrape annotations:

```yaml
prometheus.io/scrape: "true"
prometheus.io/port:   "9090"
prometheus.io/path:   "/metrics"
```

### Prometheus scrape config (plain Prometheus)

If you are running plain Prometheus (without the Operator), add the contents of
`prometheus-scrape-config.yaml` to your `prometheus.yml`:

```bash
# append to your existing scrape_configs section
cat prometheus-scrape-config.yaml >> /etc/prometheus/prometheus.yml
```

Or, on Prometheus ≥ 2.43, reference it as a separate file:

```yaml
# prometheus.yml
scrape_config_files:
  - /etc/prometheus/prometheus-scrape-config.yaml
```

The scrape config uses Kubernetes pod discovery and filters on the
`prometheus.io/scrape: "true"` annotation, so no further changes are needed
when targets are updated.

### Prometheus Operator

If you use the Prometheus Operator the `ServiceMonitor` included in
`deployment.yaml` handles scraping automatically. No additional configuration
is required.

## GitHub Actions

The workflow at `.github/workflows/build.yaml` runs on every push to `main`
and on `v*` tags:

1. Checks out the code and sets up Go 1.25 (read from `go.mod`).
2. Runs `go vet`.
3. Logs in to GHCR using the automatic `GITHUB_TOKEN` — no extra secrets
   needed.
4. Builds and pushes the image with BuildKit and GitHub Actions layer cache.

Pull requests trigger a build-only run (no push).

## Repository structure

```
.
├── main.go                      # Exporter source
├── go.mod / go.sum              # Go module files
├── Dockerfile                   # Multi-stage container build
├── deployment.yaml              # Kubernetes manifests
├── prometheus-scrape-config.yaml# Example Prometheus scrape config
└── .github/workflows/build.yaml # CI/CD pipeline
```

## License

MIT
