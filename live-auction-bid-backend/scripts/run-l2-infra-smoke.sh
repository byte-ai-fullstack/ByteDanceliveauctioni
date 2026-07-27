#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
compose_file="$repo_root/deploy/docker-compose.yml"
project_name="${L2_COMPOSE_PROJECT_NAME:-live-auction-l2-smoke}"

if ! [[ "$project_name" =~ ^[a-z0-9][a-z0-9_-]*$ ]]; then
  echo "L2_COMPOSE_PROJECT_NAME contains unsupported characters" >&2
  exit 2
fi

# Compose parses all service interpolation even when only infrastructure services are selected.
export AUCTION_TOS_ENDPOINT="${AUCTION_TOS_ENDPOINT:-http://tos.invalid}"
export AUCTION_TOS_REGION="${AUCTION_TOS_REGION:-local-test}"
export AUCTION_TOS_BUCKET="${AUCTION_TOS_BUCKET:-local-test}"
export AUCTION_TOS_ACCESS_KEY="${AUCTION_TOS_ACCESS_KEY:-local-test}"
export AUCTION_TOS_SECRET_KEY="${AUCTION_TOS_SECRET_KEY:-local-test}"

compose=(docker compose -p "$project_name" -f "$compose_file")

wait_for_metric() {
  local service="$1"
  local url="$2"
  local pattern="$3"
  local attempt
  local metrics

  for attempt in $(seq 1 30); do
    if metrics="$("${compose[@]}" exec -T nats wget -qO- "$url" 2>/dev/null)"; then
      if rg -q "$pattern" <<<"$metrics"; then
        return 0
      fi
    fi
    sleep 2
  done

  echo "$service metrics did not become ready at $url" >&2
  "${compose[@]}" logs --no-color "$service" >&2 || true
  return 1
}

cleanup() {
  if [[ "${KEEP_L2_INFRA:-0}" != "1" ]]; then
    "${compose[@]}" down --remove-orphans >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

"${compose[@]}" config --quiet
"${compose[@]}" up -d --wait mysql redis pgvector kafka nats elasticsearch
"${compose[@]}" exec -T mysql mysql -uroot -pauction_root <"$repo_root/deploy/mysql/init/10-monitoring-user.sql"
"${compose[@]}" run --rm kafka-init
"${compose[@]}" up -d --wait \
  kafka-exporter redis-exporter nats-exporter elasticsearch-exporter postgres-exporter mysql-exporter node-exporter

topic_count="$("${compose[@]}" exec -T kafka \
  /opt/kafka/bin/kafka-topics.sh --bootstrap-server kafka:29092 --list |
  rg -c '^auction\.')"
if [[ "$topic_count" != "7" ]]; then
  echo "Kafka topic count is $topic_count, want 7" >&2
  exit 1
fi
"${compose[@]}" exec -T kafka \
  /opt/kafka/bin/kafka-configs.sh \
  --bootstrap-server kafka:29092 \
  --describe \
  --entity-type topics \
  --entity-name auction.runtime.projection.v1 |
  rg -q 'retention.ms=7776000000'

redis_config="$("${compose[@]}" exec -T -e REDISCLI_AUTH=auction_redis redis \
  redis-cli --raw CONFIG GET appendonly appendfsync maxmemory maxmemory-policy stop-writes-on-bgsave-error repl-diskless-sync)"
for expected in yes everysec noeviction; do
  if ! rg -qx "$expected" <<<"$redis_config"; then
    echo "Redis runtime config is missing $expected" >&2
    exit 1
  fi
done
if ! rg -q '^[1-9][0-9]+$' <<<"$redis_config"; then
  echo "Redis maxmemory must be a positive, enforceable byte limit." >&2
  exit 1
fi

"${compose[@]}" exec -T nats wget -qO- http://127.0.0.1:8222/healthz | rg -q 'ok'
"${compose[@]}" exec -T elasticsearch curl -fsS http://127.0.0.1:9200/_cluster/health | rg -q '"status":"(green|yellow)"'
"${compose[@]}" exec -T pgvector psql -v ON_ERROR_STOP=1 -U auction_search -d live_auction_search \
  -c 'CREATE EXTENSION IF NOT EXISTS vector' \
  -c "SELECT extversion FROM pg_extension WHERE extname = 'vector'" >/dev/null

mysql_durability="$("${compose[@]}" exec -T mysql mysql -uroot -pauction_root --batch --skip-column-names \
  -e 'SELECT @@innodb_flush_log_at_trx_commit, @@sync_binlog, @@binlog_format')"
if [[ "$mysql_durability" != $'1\t1\tROW' ]]; then
  echo "unexpected MySQL durability settings: $mysql_durability" >&2
  exit 1
fi

wait_for_metric kafka-exporter http://kafka-exporter:9308/metrics '^kafka_brokers '
wait_for_metric redis-exporter http://redis-exporter:9121/metrics '^redis_up '
wait_for_metric nats-exporter http://nats-exporter:7777/metrics '^gnatsd_varz_'
wait_for_metric elasticsearch-exporter http://elasticsearch-exporter:9114/metrics '^elasticsearch_cluster_health_'
wait_for_metric postgres-exporter http://postgres-exporter:9187/metrics '^pg_up '
wait_for_metric mysql-exporter http://mysql-exporter:9104/metrics '^mysql_up '
wait_for_metric node-exporter http://node-exporter:9100/metrics '^node_uname_info'

printf 'status=passed topics=%s mysql_durability=%q\n' "$topic_count" "$mysql_durability"
