#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
base_url="${BASE_URL:-http://127.0.0.1:18080}"
gateway_metrics_url="${GATEWAY_METRICS_URL:-$base_url/metrics}"
projector_metrics_url="${PROJECTOR_METRICS_URL:-http://127.0.0.1:18083/metrics}"
relay_metrics_url="${RELAY_METRICS_URL:-http://127.0.0.1:18082/metrics}"
run_id="${RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
results_root="${RESULTS_DIR:-$repo_dir/test-results/l0-baseline}"
result_dir="$results_root/$run_id"
node_bin="${NODE_BIN:-node}"

case "$base_url" in
  http://127.0.0.1:*|http://localhost:*|https://127.0.0.1:*|https://localhost:*)
    ;;
  *)
    if [[ "${ALLOW_REMOTE_BASELINE:-0}" != "1" ]]; then
      echo "Refusing to load-test non-local BASE_URL=$base_url without ALLOW_REMOTE_BASELINE=1" >&2
      exit 2
    fi
    ;;
esac

if ! command -v "$node_bin" >/dev/null 2>&1; then
  echo "Node.js executable not found: $node_bin" >&2
  exit 2
fi
if ! command -v curl >/dev/null 2>&1; then
  echo "curl is required" >&2
  exit 2
fi

mkdir -p "$result_dir"
curl --fail --silent --show-error --retry 10 --retry-delay 1 "$base_url/readyz" >"$result_dir/ready-before.json"
curl --fail --silent --show-error "$gateway_metrics_url" >"$result_dir/metrics-gateway-before.prom"
curl --fail --silent --show-error "$projector_metrics_url" >"$result_dir/metrics-projector-before.prom"
curl --fail --silent --show-error "$relay_metrics_url" >"$result_dir/metrics-relay-before.prom"

{
  printf 'generated_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf 'run_id=%s\n' "$run_id"
  printf 'base_url=%s\n' "$base_url"
  printf 'gateway_metrics_url=%s\n' "$gateway_metrics_url"
  printf 'projector_metrics_url=%s\n' "$projector_metrics_url"
  printf 'relay_metrics_url=%s\n' "$relay_metrics_url"
  printf 'git_commit=%s\n' "$(git -C "$repo_dir" rev-parse HEAD)"
  printf 'node=%s\n' "$($node_bin --version)"
  printf 'kernel=%s\n' "$(uname -srmo)"
  if command -v docker >/dev/null 2>&1; then
    printf 'docker_client=%s\n' "$(docker version --format '{{.Client.Version}}' 2>/dev/null || true)"
    printf 'docker_server=%s\n' "$(docker version --format '{{.Server.Version}}' 2>/dev/null || true)"
  fi
} >"$result_dir/environment.txt"

echo "Writing L0 baseline evidence to $result_dir"
BASE_URL="$base_url" \
GATEWAY_METRICS_URL="$gateway_metrics_url" \
PROJECTOR_METRICS_URL="$projector_metrics_url" \
RELAY_METRICS_URL="$relay_metrics_url" \
RUN_ID="$run_id" \
CONCURRENCY="${CONCURRENCY:-100}" \
WS_CONNECTIONS="${WS_CONNECTIONS:-100}" \
REPORT_FILE="$result_dir/report.json" \
"$node_bin" "$repo_dir/scripts/load-bid-hot-path.mjs" | tee "$result_dir/stdout.log"

curl --fail --silent --show-error "$gateway_metrics_url" >"$result_dir/metrics-gateway-after.prom"
curl --fail --silent --show-error "$projector_metrics_url" >"$result_dir/metrics-projector-after.prom"
curl --fail --silent --show-error "$relay_metrics_url" >"$result_dir/metrics-relay-after.prom"
curl --fail --silent --show-error "$base_url/readyz" >"$result_dir/ready-after.json"
(
  cd "$result_dir"
  sha256sum \
    environment.txt \
    metrics-gateway-before.prom metrics-gateway-after.prom \
    metrics-projector-before.prom metrics-projector-after.prom \
    metrics-relay-before.prom metrics-relay-after.prom \
    ready-before.json ready-after.json report.json stdout.log >sha256sums.txt
)

echo "L0 baseline completed: $result_dir/report.json"
