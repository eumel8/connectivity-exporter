"""Assert a successful probe and exclusive target selection in Prometheus text."""

import re
import sys

expected, forbidden = sys.argv[1:]
metrics = {}
for line in sys.stdin:
    if line.startswith("#") or not line.strip():
        continue
    sample, value = line.split()[:2]
    metrics[sample] = float(value)

up = f'connectivity_up{{target="{expected}"}}'
latency = f'connectivity_latency_seconds{{target="{expected}"}}'
assert metrics.get(up) == 1, f"Expected {up} 1, got {metrics.get(up)}"
assert metrics.get(latency, -1) >= 0, f"Missing or negative latency for {expected}"

# Label order is not significant in the Prometheus exposition format.
successes = [
    value
    for sample, value in metrics.items()
    if sample.startswith("connectivity_checks_total{")
    and f'target="{expected}"' in sample
    and 'result="success"' in sample
]
assert successes and sum(successes) >= 1, f"No successful checks for {expected}"
assert not any(
    sample.startswith("connectivity_")
    and re.search(r'target="' + re.escape(forbidden) + r'"', sample)
    for sample in metrics
), f"Unexpected metrics for overridden target {forbidden}"
print(f"PASS: {expected} is reachable; {forbidden} is absent")
