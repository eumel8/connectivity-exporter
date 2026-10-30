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
| `TARGETS` | unless URL or file provides valid targets at startup | — | Comma-separated list of `host:port` targets, e.g. `1.2.3.4:443,example.com:80`; startup fallback |
| `TARGETS_URL` | no | — | HTTP(S) URL serving a plain-text target list; overrides file/env targets and refreshes automatically |
| `TARGETS_POLL_INTERVAL` | no | `300` | Refresh interval in seconds (1–86400), with ±20% random jitter |
| `TARGETS_FILE` | no | — | Path to a plain-text target list; valid targets override `TARGETS`. Supports one target per line or comma-separated targets |
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
2. **Final** — `scratch` base image; contains the binary and CA certificates, running as UID
   `65534` (nobody).

## Kubernetes deployment

### Deploy

Edit the `TARGETS` value in `deployment.yaml` to match your fallback endpoints.
Optionally set `TARGETS_URL` in the exporter container to an HTTP(S)
URL serving a plain-text target list, then apply:

```bash
kubectl apply -f deployment.yaml
```

### Automatic target refresh

The exporter downloads `TARGETS_URL` itself; no init container or shared volume
is needed. Startup priority is `TARGETS_URL` → `TARGETS_FILE` → `TARGETS`.
The optional `TARGETS_FILE` remains supported as a startup-only fallback for
existing installations. If no source provides valid targets, startup fails.

Example file served by the source server:

```text
example.com:443
10.0.0.1:8080
[2001:db8::1]:443
```

Comma-separated targets are also supported. Blank entries are ignored; invalid
targets are logged and skipped. A file with some valid targets still overrides
the entire fallback list; the lists are not merged.

After the initial download, the exporter refreshes at `TARGETS_POLL_INTERVAL`
(default: 300 seconds), randomized by ±20% on each request to spread load across
clusters. The first request happens immediately at startup. Downloads have a
15-second total HTTP timeout and a maximum size of 1 MiB. HTTPS uses the bundled
CA certificates; a custom trust bundle can be mounted and selected via Go's
`SSL_CERT_FILE` environment variable.

When supplied by the server, `ETag` is sent back as `If-None-Match`; otherwise
`Last-Modified` is used with `If-Modified-Since`. HTTP 304 keeps the active list
without downloading a body. Servers without validators work with regular GETs.
See [RFC 9110 conditional requests](https://www.rfc-editor.org/rfc/rfc9110.html#section-13).

Successful updates replace the active list without a Pod restart. Metrics for
removed targets disappear; counters for retained targets continue. Existing
checks finish before a replacement is applied, and new targets are probed on
the next check cycle. Failed, empty, oversized, truncated, or entirely invalid
responses keep the last accepted list (or the startup fallback until the first
successful download). The last accepted list is held in memory, not persisted
across process restarts. An empty file does not disable all probes.

When upgrading from v0.1.0, move `TARGETS_URL` from the init container to the
exporter container and remove the `fetch-targets` init container, shared volume,
and `TARGETS_FILE` setting if they were used only for downloading targets.

### Created resources

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

### End-to-end tests

The separate `.github/workflows/e2e.yaml` workflow runs on pull requests, pushes
to `main`, and manual dispatch. It builds the exporter image and loads it into
a disposable kind cluster running Kubernetes **1.36.4**.

An nginx Deployment serves a mounted text file containing its own Pod IP and
port. The test deploys the real `deployment.yaml` with that target URL, then
queries the exporter's `/metrics` endpoint from a separate curl Pod. It checks
that the downloaded target is up, has non-negative latency and successful
checks, and that the fallback target is absent. It changes the served file and
verifies live target replacement, removal of old metrics, preservation during
empty/invalid/HTTP 404 responses, recovery and conditional HTTP 304 requests.
Pod UID and restart count prove that updates require no restart. Separate
startup cases verify fallback to `TARGETS` with an empty file and HTTP 404.
Go tests with the race detector cover validators and metric cleanup.

Any failed assertion or rollout timeout fails the job. On failure, Pod details,
events, nginx logs, and exporter logs are printed for diagnosis.

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
