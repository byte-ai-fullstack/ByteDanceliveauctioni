#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
prometheus_image="prom/prometheus:v2.53.1@sha256:f20d3127bf2876f4a1df76246fca576b41ddf1125ed1c546fbd8b16ea55117e6"
overview_dashboard="$repo_root/deploy/grafana/dashboards/live-auction-overview.json"

jq -e '
  .uid == "live-auction-target-infra" and
  .title == "Live Auction · Target Infrastructure" and
  ([.panels[].id] | length == (unique | length)) and
  ([.panels[].targets[]?.expr] | length >= 10)
' "$repo_root/deploy/grafana/dashboards/target-infrastructure.json" >/dev/null
rg -q -- 'auction_projection_gate_ready' "$repo_root/deploy/grafana/dashboards/target-infrastructure.json"
rg -q -- 'auction_projection_gate_retention_headroom_ms' "$repo_root/deploy/grafana/dashboards/target-infrastructure.json"
rg -q -- 'auction_search_retrieval_duration_ms_bucket' "$repo_root/deploy/grafana/dashboards/target-infrastructure.json"
jq -e '.title | length > 0' "$overview_dashboard" >/dev/null
rg -q -- 'auction_outbox_ack_result_total[^\n]*result=\\"ok\\"' "$overview_dashboard"
if rg -q -- 'auction_outbox_ack_result_total[^\n]*result=\\"OK\\"' "$overview_dashboard"; then
  echo 'Grafana Relay ACK panels must use the emitted lowercase result="ok" label' >&2
  exit 1
fi

for exporter in kafka-exporter redis-exporter nats-exporter elasticsearch-exporter postgres-exporter mysql-exporter node-exporter; do
  if ! rg -q -- "^  ${exporter}:$" "$repo_root/deploy/prod/docker-compose.yml"; then
    echo "production Compose is missing ${exporter}" >&2
    exit 1
  fi
done
rg -q -- 'prometheus\.prod\.yml:/etc/prometheus/prometheus\.yml:ro' "$repo_root/deploy/prod/docker-compose.yml"
rg -q -- 'deploy/prometheus/rules:/etc/prometheus/rules:ro' "$repo_root/deploy/prod/docker-compose.yml"
rg -q -- 'target-infrastructure\.yml' "$repo_root/deploy/prometheus/prometheus.prod.yml"
rg -q -- 'AUCTION_REDIS_MAXMEMORY' "$repo_root/deploy/docker-compose.yml"
rg -q -- 'AUCTION_REDIS_MAXMEMORY:\?AUCTION_REDIS_MAXMEMORY is required' "$repo_root/deploy/prod/docker-compose.yml"
rg -q -- '^maxmemory [1-9][0-9]*(kb|mb|gb)$' "$repo_root/deploy/redis/ha.conf"
rg -q -- '^maxmemory-policy noeviction$' "$repo_root/deploy/redis/ha.conf"
rg -q -- 'CONFIG GET .*maxmemory' "$repo_root/scripts/run-l2-infra-smoke.sh"
for alert in AuctionProjectionGateUnavailable AuctionProjectionGateDegraded AuctionProjectionGateRejectingCommands AuctionProjectionRetentionHeadroomLow AuctionProjectionOldestAgeHigh AuctionBuyerSearchRetrievalP99High; do
  rg -q -- "alert: ${alert}" "$repo_root/deploy/prometheus/rules/target-infrastructure.yml"
done

for config in prometheus.yml prometheus.prod.yml; do
  docker run --rm \
    --entrypoint /bin/promtool \
    -v "$repo_root/deploy/prometheus:/etc/prometheus:ro" \
    "$prometheus_image" \
    check config "/etc/prometheus/${config}"
done

docker run --rm \
  --entrypoint /bin/promtool \
  -v "$repo_root:/workspace:ro" \
  -w /workspace \
  "$prometheus_image" \
  test rules test/infra/prometheus-rules.test.yml
