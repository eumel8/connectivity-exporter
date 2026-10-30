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

# Use the real deployment, including its security context.
kubectl patch --local -f deployment.yaml \
  --patch-file .github/e2e/exporter-patch.yaml -o yaml | k apply -f -

assert_metrics() {
  local expected=$1 forbidden=$2 exporter_ip metrics
  k rollout status deployment/connectivity-exporter --timeout=120s
  # A completed rollout can still leave terminating predecessor Pods listed.
  exporter_ip=$(k get pods -l app=connectivity-exporter -o json | python3 -c '
import json, sys
pods = [p for p in json.load(sys.stdin)["items"] if not p["metadata"].get("deletionTimestamp")]
assert len(pods) == 1, f"Expected one active exporter Pod, got {len(pods)}"
print(pods[0]["status"]["podIP"])
')
  # Readiness is not proof of a completed probe; allow up to 15 scrape attempts.
  for attempt in {1..15}; do
    if metrics=$(k exec metrics-client -- curl --fail --silent --show-error \
      --max-time 5 "http://${exporter_ip}:9090/metrics"); then
      if python3 .github/e2e/assert-metrics.py "$expected" "$forbidden" <<< "$metrics"; then
        python3 -c 'import sys; [print(line.rstrip()) for line in sys.stdin if line.startswith("connectivity_")]' <<< "$metrics"
        return 0
      fi
    fi
    sleep 2
  done
  return 1
}

assert_metrics "$target" "$fallback"

# Change only nginx content: the exporter must reload without a Pod restart.
before=$(k get pods -l app=connectivity-exporter -o jsonpath='{.items[0].metadata.uid}')
updated_target="nginx:80"
k exec deployment/nginx -- sh -ec \
  'printf "%s\n" "$1" > /usr/share/nginx/html/new.txt; mv /usr/share/nginx/html/new.txt /usr/share/nginx/html/targets.txt' sh "$updated_target"
assert_metrics "$updated_target" "$target"

# Empty, invalid and missing files must preserve the last accepted list.
for content in '' 'invalid-target'; do
  k exec deployment/nginx -- sh -ec \
    'printf "%s\n" "$1" > /usr/share/nginx/html/targets.txt' sh "$content"
  sleep 4
  assert_metrics "$updated_target" "$fallback"
done
k exec deployment/nginx -- rm /usr/share/nginx/html/targets.txt
sleep 4
assert_metrics "$updated_target" "$fallback"

# Recovery must adopt a new list and then use conditional requests (nginx 304).
k exec deployment/nginx -- sh -ec \
  'printf "%s\n" "$1" > /usr/share/nginx/html/targets.txt' sh "$target"
assert_metrics "$target" "$updated_target"
sleep 4
k logs deployment/nginx -c nginx | python3 -c '
import sys
logs = sys.stdin.read()
assert "GET /targets.txt HTTP/1.1\" 304 " in logs, "No conditional HTTP 304 observed"
'
after=$(k get pods -l app=connectivity-exporter -o jsonpath='{.items[0].metadata.uid}')
test "$before" = "$after"
test "$(k get pods -l app=connectivity-exporter -o jsonpath='{.items[0].status.containerStatuses[0].restartCount}')" = 0

# Startup fallback is still required when no valid download has been accepted.
k set env deployment/connectivity-exporter \
  TARGETS_URL=http://nginx/empty.txt
assert_metrics "$fallback" "$target"

k set env deployment/connectivity-exporter \
  TARGETS_URL=http://nginx/missing.txt
assert_metrics "$fallback" "$target"
