#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
fixture_bin="$repo_root/test/infra/fixtures"
initializer="$repo_root/scripts/kafka-apply-acls.sh"
scratch_dir="$(mktemp -d)"
trap 'rm -rf -- "$scratch_dir"' EXIT
touch "$scratch_dir/admin.properties"

KAFKA_BIN_DIR="$fixture_bin" \
  KAFKA_FAKE_LOG="$scratch_dir/kafka.log" \
  KAFKA_BOOTSTRAP_SERVERS="kafka.test:9092" \
  KAFKA_COMMAND_CONFIG="$scratch_dir/admin.properties" \
  "$initializer"

rg -q -- '--allow-principal User:outbox-relay --operation Write --topic auction.runtime.projection.v1' "$scratch_dir/kafka.log"
rg -q -- '--allow-principal User:auction-service --operation Read --topic auction.runtime.projection.v1' "$scratch_dir/kafka.log"
rg -q -- '--allow-principal User:auction-service --operation Describe --topic auction.runtime.projection.v1' "$scratch_dir/kafka.log"
if rg -q -- '--allow-principal User:auction-service .*--operation Write|--allow-principal User:auction-service .*--group' "$scratch_dir/kafka.log"; then
  echo "auction-service unexpectedly received a Kafka write or group ACL" >&2
  exit 1
fi
rg -q -- '--allow-principal User:projector --operation Read --group auction-projector-v1' "$scratch_dir/kafka.log"
rg -q -- '--allow-principal User:projection-repair --operation Read --topic auction.runtime.projection.v1' "$scratch_dir/kafka.log"
if rg -q -- '--allow-principal User:projection-repair --operation Write' "$scratch_dir/kafka.log"; then
  echo "projection-repair unexpectedly received a Kafka write ACL" >&2
  exit 1
fi
rg -q -- '--allow-principal User:domain-relay --operation Write --topic auction.order.created.v1' "$scratch_dir/kafka.log"
rg -q -- '--allow-principal User:index-es --operation Read --topic auction.lot.state.v1' "$scratch_dir/kafka.log"
rg -q -- '--allow-principal User:index-es --operation Read --group search-es-v1' "$scratch_dir/kafka.log"
rg -q -- '--allow-principal User:index-pgvector --operation Read --group search-pgvector-v1' "$scratch_dir/kafka.log"
rg -q -- '--allow-principal User:enrichment-consumer --operation Read --group auction-order-enrichment-v1' "$scratch_dir/kafka.log"
rg -q -- '--allow-principal User:monitoring --operation Describe --cluster' "$scratch_dir/kafka.log"

if KAFKA_BIN_DIR="$fixture_bin" KAFKA_FAKE_LOG="$scratch_dir/kafka.log" KAFKA_BOOTSTRAP_SERVERS=kafka.test:9092 KAFKA_COMMAND_CONFIG="$scratch_dir/missing.properties" "$initializer" 2>/dev/null; then
  echo "ACL initializer must reject an unreadable command config" >&2
  exit 1
fi
