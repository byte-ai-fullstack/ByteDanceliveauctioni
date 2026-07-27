#!/usr/bin/env bash
set -euo pipefail

bootstrap_servers="${KAFKA_BOOTSTRAP_SERVERS:-kafka:29092}"
replication_factor="${KAFKA_REPLICATION_FACTOR:-1}"
min_insync_replicas="${KAFKA_MIN_INSYNC_REPLICAS:-1}"
kafka_bin_dir="${KAFKA_BIN_DIR:-/opt/kafka/bin}"
command_config_args=()
if [[ -n "${KAFKA_COMMAND_CONFIG:-}" ]]; then
  command_config_args=(--command-config "$KAFKA_COMMAND_CONFIG")
fi

if ! [[ "$replication_factor" =~ ^[1-9][0-9]*$ ]]; then
  echo "KAFKA_REPLICATION_FACTOR must be a positive integer" >&2
  exit 2
fi
if ! [[ "$min_insync_replicas" =~ ^[1-9][0-9]*$ ]] || ((min_insync_replicas > replication_factor)); then
  echo "KAFKA_MIN_INSYNC_REPLICAS must be positive and no greater than the replication factor" >&2
  exit 2
fi

create_topic() {
  local topic="$1"
  local partitions="$2"
  local retention_ms="$3"

  "$kafka_bin_dir/kafka-topics.sh" \
    --bootstrap-server "$bootstrap_servers" \
    "${command_config_args[@]}" \
    --create \
    --if-not-exists \
    --topic "$topic" \
    --partitions "$partitions" \
    --replication-factor "$replication_factor" \
    --config "cleanup.policy=delete" \
    --config "retention.ms=$retention_ms" \
    --config "min.insync.replicas=$min_insync_replicas"

  local description actual_partitions actual_replication_factor
  description="$(
    "$kafka_bin_dir/kafka-topics.sh" \
      --bootstrap-server "$bootstrap_servers" \
      "${command_config_args[@]}" \
      --describe \
      --topic "$topic"
  )"
  actual_partitions="$(sed -n 's/.*PartitionCount: \([0-9][0-9]*\).*/\1/p' <<<"$description" | head -n 1)"
  actual_replication_factor="$(sed -n 's/.*ReplicationFactor: \([0-9][0-9]*\).*/\1/p' <<<"$description" | head -n 1)"
  if [[ -z "$actual_partitions" || -z "$actual_replication_factor" ]]; then
    echo "unable to determine partition count and replication factor for $topic" >&2
    exit 1
  fi
  if ((actual_partitions != partitions)); then
    echo "$topic has $actual_partitions partitions; expected immutable count $partitions" >&2
    echo "refusing implicit partition remapping; follow docs/KAFKA_PARTITION_MIGRATION_RUNBOOK.md" >&2
    exit 1
  fi
  if ((actual_replication_factor != replication_factor)); then
    echo "$topic has replication factor $actual_replication_factor; expected $replication_factor" >&2
    echo "refusing implicit replica reassignment; use an approved Kafka reassignment plan" >&2
    exit 1
  fi

  "$kafka_bin_dir/kafka-configs.sh" \
    --bootstrap-server "$bootstrap_servers" \
    "${command_config_args[@]}" \
    --alter \
    --entity-type topics \
    --entity-name "$topic" \
    --add-config "cleanup.policy=delete,retention.ms=$retention_ms,min.insync.replicas=$min_insync_replicas"
}

create_topic auction.runtime.projection.v1 24 7776000000
create_topic auction.bid.accepted.v1 24 604800000
create_topic auction.lot.settled.v1 12 2592000000
create_topic auction.order.created.v1 12 2592000000
create_topic auction.lot.state.v1 12 2592000000
create_topic auction.order.enrichment.requested.v1 12 2592000000
create_topic auction.dlq.v1 3 2592000000
