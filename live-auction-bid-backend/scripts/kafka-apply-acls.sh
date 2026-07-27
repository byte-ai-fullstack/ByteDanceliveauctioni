#!/usr/bin/env bash
set -euo pipefail

bootstrap_servers="${KAFKA_BOOTSTRAP_SERVERS:?KAFKA_BOOTSTRAP_SERVERS is required}"
command_config="${KAFKA_COMMAND_CONFIG:?KAFKA_COMMAND_CONFIG is required}"
kafka_bin_dir="${KAFKA_BIN_DIR:-/opt/kafka/bin}"

if [[ ! -r "$command_config" ]]; then
  echo "KAFKA_COMMAND_CONFIG must reference a readable admin client properties file" >&2
  exit 2
fi

add_topic_acl() {
  local principal="$1"
  local operation="$2"
  local topic="$3"
  "$kafka_bin_dir/kafka-acls.sh" \
    --bootstrap-server "$bootstrap_servers" \
    --command-config "$command_config" \
    --add \
    --allow-principal "User:$principal" \
    --operation "$operation" \
    --topic "$topic"
}

add_group_acl() {
  local principal="$1"
  local operation="$2"
  local group="$3"
  "$kafka_bin_dir/kafka-acls.sh" \
    --bootstrap-server "$bootstrap_servers" \
    --command-config "$command_config" \
    --add \
    --allow-principal "User:$principal" \
    --operation "$operation" \
    --group "$group"
}

add_cluster_acl() {
  local principal="$1"
  local operation="$2"
  "$kafka_bin_dir/kafka-acls.sh" \
    --bootstrap-server "$bootstrap_servers" \
    --command-config "$command_config" \
    --add \
    --allow-principal "User:$principal" \
    --operation "$operation" \
    --cluster
}

add_topic_acl outbox-relay Write auction.runtime.projection.v1
add_topic_acl outbox-relay Describe auction.runtime.projection.v1

# The auction-service projection gate uses direct partition reads only. It has
# no consumer group, cannot commit offsets, and cannot write any Kafka topic.
add_topic_acl auction-service Read auction.runtime.projection.v1
add_topic_acl auction-service Describe auction.runtime.projection.v1

add_topic_acl projector Read auction.runtime.projection.v1
add_topic_acl projector Describe auction.runtime.projection.v1
add_group_acl projector Read auction-projector-v1

# One-shot projection repair uses direct partition assignment. It may inspect
# only the canonical Runtime Topic and owns no consumer group or write ACL.
add_topic_acl projection-repair Read auction.runtime.projection.v1
add_topic_acl projection-repair Describe auction.runtime.projection.v1

for topic in \
  auction.bid.accepted.v1 \
  auction.lot.settled.v1 \
  auction.order.created.v1 \
  auction.lot.state.v1 \
  auction.order.enrichment.requested.v1 \
  auction.dlq.v1; do
  add_topic_acl domain-relay Write "$topic"
  add_topic_acl domain-relay Describe "$topic"
done

add_topic_acl enrichment-consumer Read auction.order.enrichment.requested.v1
add_topic_acl enrichment-consumer Write auction.dlq.v1
add_group_acl enrichment-consumer Read auction-order-enrichment-v1

for principal in index-es index-pgvector; do
  add_topic_acl "$principal" Read auction.lot.state.v1
  add_topic_acl "$principal" Write auction.dlq.v1
done
add_group_acl index-es Read search-es-v1
add_group_acl index-pgvector Read search-pgvector-v1

# One-shot rebuild uses direct partition assignment and therefore needs no
# consumer-group ACL. It can only describe/read the canonical lot-state topic.
add_topic_acl search-rebuild Read auction.lot.state.v1
add_topic_acl search-rebuild Describe auction.lot.state.v1

# Reconciliation republishes only a previously validated canonical lot-state
# payload. It owns no consumer group and cannot write any other topic.
add_topic_acl search-reconciler Write auction.lot.state.v1
add_topic_acl search-reconciler Describe auction.lot.state.v1

for principal in outbox-relay domain-relay enrichment-consumer index-es index-pgvector search-reconciler; do
  add_cluster_acl "$principal" IdempotentWrite
done

add_cluster_acl monitoring Describe
add_topic_acl monitoring Describe '*'
add_group_acl monitoring Describe '*'
add_group_acl monitoring Read '*'
