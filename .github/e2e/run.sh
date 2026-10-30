#!/usr/bin/env bash
set -euo pipefail

# Every operation explicitly targets the disposable Actions cluster.
k() {
  kubectl --context kind-connectivity-e2e --namespace connectivity-e2e "$@"
}

test "$(kubectl config current-context)" = kind-connectivity-e2e
kubectl --context kind-connectivity-e2e get nodes -o wide
kubectl --context kind-connectivity-e2e create namespace connectivity-e2e
k apply -f .github/e2e/nginx.yaml
k rollout status deployment/nginx --timeout=120s
nginx_ip=$(k get pods -l app=nginx -o jsonpath='{.items[0].status.podIP}')
test -n "$nginx_ip"
target="${nginx_ip}:80"
fallback="127.0.0.1:9090"

k run metrics-client --image=curlimages/curl:8.12.1 --restart=Never \
  --command -- /bin/sh -ec 'sleep 900'
k wait --for=condition=Ready pod/metrics-client --timeout=120s

# Confirm nginx serves the mounted file containing its own Pod IP.
downloaded=$(k exec metrics-client -- curl --fail --silent --show-error \
  --max-time 10 http://nginx/targets.txt)
test "$downloaded" = "$target"

# Use the real deployment, including its init script and security context.
kubectl patch --local -f deployment.yaml \
  --patch-file .github/e2e/exporter-patch.yaml -o yaml | k apply -f -

assert_metrics() {
  local expected=$1 forbidden=$2 exporter_ip metrics
  k rollout status deployment/connectivity-exporter --timeout=120s
  exporter_ip=$(k get pods -l app=connectivity-exporter \
    -o jsonpath='{.items[0].status.podIP}')
  # Readiness is not proof of a completed probe; retry assertions for up to 30s.
  for attempt in {1..15}; do
    if metrics=$(k exec metrics-client -- curl --fail --silent --show-error \
      --max-time 5 "http://${exporter_ip}:9090/metrics"); then
      if python3 .github/e2e/assert-metrics.py "$expected" "$forbidden" <<< "$metrics"; then
        printf '%s\n' "$metrics"
        return 0
      fi
    fi
    sleep 2
  done
  return 1
}

assert_metrics "$target" "$fallback"

# Recreate the exporter via a template change so the init container runs again.
k set env deployment/connectivity-exporter --containers=fetch-targets \
  TARGETS_URL=http://nginx/empty.txt
assert_metrics "$fallback" "$target"

k set env deployment/connectivity-exporter --containers=fetch-targets \
  TARGETS_URL=http://nginx/missing.txt
assert_metrics "$fallback" "$target"
