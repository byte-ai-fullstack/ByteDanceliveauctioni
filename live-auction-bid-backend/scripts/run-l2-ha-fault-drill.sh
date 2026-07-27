#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
compose_file="$repo_root/deploy/ha/docker-compose.yml"
project_name="${L2_HA_PROJECT_NAME:-live-auction-l2-ha-${GITHUB_RUN_ID:-manual}}"
redis_password="redis-ha-test-only"
drill_id="${GITHUB_RUN_ID:-manual}-$(date -u +%s)"

if ! [[ "$project_name" =~ ^[a-z0-9][a-z0-9_-]*$ ]]; then
  echo "L2_HA_PROJECT_NAME contains unsupported characters" >&2
  exit 2
fi

compose=(docker compose -p "$project_name" -f "$compose_file")
all_services=(
  kafka-1 kafka-2 kafka-3
  redis-primary redis-replica-1 redis-replica-2
  sentinel-1 sentinel-2 sentinel-3
  nats-1 nats-2 nats-3
)

cleanup() {
  "${compose[@]}" unpause "${all_services[@]}" >/dev/null 2>&1 || true
  "${compose[@]}" start "${all_services[@]}" >/dev/null 2>&1 || true
  if [[ "${KEEP_L2_HA:-0}" != "1" ]]; then
    "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

wait_for_kafka() {
  local service="$1"
  local attempt
  for attempt in $(seq 1 30); do
    if "${compose[@]}" exec -T "$service" \
      /opt/kafka/bin/kafka-broker-api-versions.sh --bootstrap-server 127.0.0.1:9092 >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  echo "$service did not become Kafka-ready" >&2
  return 1
}

produce_marker() {
  local service="$1"
  local phase="$2"
  local timeout_seconds="${L2_HA_PRODUCER_TIMEOUT_SECONDS:-30}"
  printf '%s:%s\n' "$drill_id" "$phase" |
    timeout "${timeout_seconds}s" "${compose[@]}" exec -T "$service" \
    /opt/kafka/bin/kafka-console-producer.sh \
    --bootstrap-server 127.0.0.1:9092 \
    --topic auction.dlq.v1 \
    --sync \
    --property parse.key=true \
    --property key.separator=: \
    --producer-property acks=all \
    --producer-property request.timeout.ms=5000 \
    --producer-property delivery.timeout.ms=8000 \
    --producer-property "client.id=l2-ha-$phase"
}

produce_marker_eventually() {
  local service="$1"
  local phase="$2"
  local attempt
  for attempt in $(seq 1 "${L2_HA_PRODUCER_ATTEMPTS:-6}"); do
    if produce_marker "$service" "$phase"; then
      return 0
    fi
    sleep 2
  done
  echo "Kafka did not accept $phase after bounded failover retries" >&2
  return 1
}

assert_kafka_markers() {
  local consumer_log
  local attempt
  consumer_log="$(mktemp)"
  for attempt in $(seq 1 "${L2_HA_CONSUMER_ATTEMPTS:-6}"); do
    : >"$consumer_log"
    "${compose[@]}" exec -T kafka-3 \
      /opt/kafka/bin/kafka-console-consumer.sh \
      --bootstrap-server 127.0.0.1:9092 \
      --topic auction.dlq.v1 \
      --from-beginning \
      --timeout-ms "${L2_HA_CONSUMER_TIMEOUT_MS:-10000}" \
      --property print.key=true \
      --property key.separator=: >"$consumer_log" 2>&1 || true
    if rg -Fq "$drill_id:baseline" "$consumer_log" && rg -Fq "$drill_id:one-broker-down" "$consumer_log"; then
      rm -f "$consumer_log"
      return 0
    fi
    sleep 2
  done
  echo "Kafka failover did not retain both acknowledged drill records after bounded recovery retries" >&2
  sed -n '1,80p' "$consumer_log" >&2
  rm -f "$consumer_log"
  return 1
}

sentinel_master() {
  "${compose[@]}" exec -T sentinel-1 \
    redis-cli -h sentinel-1 -p 26379 --raw SENTINEL get-master-addr-by-name live-auction |
    sed -n '1p' |
    tr -d '\r'
}

redis_run_id() {
  local host="$1"
  "${compose[@]}" exec -T sentinel-1 \
    redis-cli -h "$host" -a "$redis_password" --no-auth-warning --raw INFO server |
    sed -n 's/^run_id://p' |
    tr -d '\r'
}

wait_for_redis_replica() {
  local host="$1"
  local attempt
  local replication
  for attempt in $(seq 1 45); do
    replication="$("${compose[@]}" exec -T sentinel-1 \
      redis-cli -h "$host" -a "$redis_password" --no-auth-warning --raw INFO replication 2>/dev/null || true)"
    if rg -q '^role:slave\r?$' <<<"$replication" && rg -q '^master_link_status:up\r?$' <<<"$replication"; then
      return 0
    fi
    sleep 2
  done
  echo "$host did not rejoin the promoted Redis primary as a healthy replica" >&2
  return 1
}

wait_for_nats_routes() {
  local service="$1"
  local minimum="$2"
  local attempt
  for attempt in $(seq 1 30); do
    if "${compose[@]}" exec -T "$service" wget -qO- http://127.0.0.1:8222/routez 2>/dev/null |
      jq -e --argjson minimum "$minimum" '.num_routes >= $minimum' >/dev/null; then
      return 0
    fi
    sleep 2
  done
  echo "$service did not reach $minimum NATS routes" >&2
  return 1
}

"${compose[@]}" config --quiet
"${compose[@]}" up -d --wait --wait-timeout "${L2_HA_STARTUP_TIMEOUT_SECONDS:-180}" "${all_services[@]}"
"${compose[@]}" run --rm kafka-init

topic_description="$("${compose[@]}" exec -T kafka-1 \
  /opt/kafka/bin/kafka-topics.sh --bootstrap-server kafka-1:9092 --describe --topic auction.dlq.v1)"
rg -q 'ReplicationFactor: 3' <<<"$topic_description"
topic_config="$("${compose[@]}" exec -T kafka-1 \
  /opt/kafka/bin/kafka-configs.sh --bootstrap-server kafka-1:9092 --describe \
  --entity-type topics --entity-name auction.dlq.v1)"
rg -q 'min.insync.replicas=2' <<<"$topic_config"

produce_marker kafka-1 baseline
"${compose[@]}" stop kafka-1 >/dev/null
wait_for_kafka kafka-2
produce_marker_eventually kafka-2 one-broker-down

"${compose[@]}" stop kafka-2 >/dev/null
kafka_failure_log="$(mktemp)"
if produce_marker kafka-3 two-brokers-down >"$kafka_failure_log" 2>&1; then
  echo "Kafka accepted acks=all with only one of three replicas available" >&2
  rm -f "$kafka_failure_log"
  exit 1
fi
rm -f "$kafka_failure_log"
"${compose[@]}" stop -t 10 kafka-3 >/dev/null
"${compose[@]}" start kafka-1 kafka-2 kafka-3 >/dev/null
wait_for_kafka kafka-1
wait_for_kafka kafka-2
wait_for_kafka kafka-3
assert_kafka_markers
printf 'component=kafka status=passed rf=3 min_isr=2 one_failure=available two_failures=rejected acknowledged_records_retained=true\n'

redis_key="l2:ha:drill:${GITHUB_RUN_ID:-manual}"
old_master="$(sentinel_master)"
old_run_id="$(redis_run_id "$old_master")"
redis_results="$(
  printf 'SET %s retained\nWAIT 1 10000\n' "$redis_key" |
    "${compose[@]}" exec -T sentinel-1 \
      redis-cli -h "$old_master" -a "$redis_password" --no-auth-warning --raw
)"
if ! tail -n 1 <<<"$redis_results" | rg -q '^[1-9][0-9]*$'; then
  echo "Redis did not confirm replication before failover: $redis_results" >&2
  exit 1
fi

"${compose[@]}" pause "$old_master" >/dev/null
new_master=""
for _ in $(seq 1 45); do
  candidate="$(sentinel_master || true)"
  if [[ -n "$candidate" && "$candidate" != "$old_master" ]]; then
    new_master="$candidate"
    break
  fi
  sleep 2
done
if [[ -z "$new_master" ]]; then
  echo "Redis Sentinel did not promote a replacement for $old_master" >&2
  exit 1
fi
if [[ "$("${compose[@]}" exec -T sentinel-1 redis-cli -h "$new_master" -a "$redis_password" --no-auth-warning --raw GET "$redis_key")" != "retained" ]]; then
  echo "Redis failover lost the acknowledged drill key" >&2
  exit 1
fi
new_run_id="$(redis_run_id "$new_master")"
if [[ -z "$old_run_id" || -z "$new_run_id" || "$old_run_id" == "$new_run_id" ]]; then
  echo "Redis failover did not expose a changed run_id generation" >&2
  exit 1
fi
"${compose[@]}" unpause "$old_master" >/dev/null
wait_for_redis_replica "$old_master"
printf 'component=redis status=passed old_master=%s new_master=%s generation_changed=true old_master_rejoined_as_replica=true\n' "$old_master" "$new_master"

wait_for_nats_routes nats-1 2
"${compose[@]}" stop nats-1 >/dev/null
wait_for_nats_routes nats-2 1
wait_for_nats_routes nats-3 1
"${compose[@]}" start nats-1 >/dev/null
wait_for_nats_routes nats-1 2
printf 'component=nats status=passed routes_after_one_failure=1 restored_routes=2\n'

printf 'status=passed drill=l2-ha project=%s\n' "$project_name"
