#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
fixture_bin="$repo_root/test/infra/fixtures"
initializer="$repo_root/scripts/kafka-init-topics.sh"
scratch_dir="$(mktemp -d)"
trap 'rm -rf -- "$scratch_dir"' EXIT

run_initializer() {
  KAFKA_BIN_DIR="$fixture_bin" \
    KAFKA_FAKE_LOG="$scratch_dir/kafka.log" \
    KAFKA_BOOTSTRAP_SERVERS="kafka.test:29092" \
    KAFKA_REPLICATION_FACTOR="${KAFKA_REPLICATION_FACTOR:-1}" \
    KAFKA_MIN_INSYNC_REPLICAS="${KAFKA_MIN_INSYNC_REPLICAS:-1}" \
    KAFKA_FAKE_MODE="${KAFKA_FAKE_MODE:-expected}" \
    KAFKA_FAKE_ACTUAL_REPLICATION_FACTOR="${KAFKA_FAKE_ACTUAL_REPLICATION_FACTOR:-${KAFKA_REPLICATION_FACTOR:-1}}" \
    "$initializer"
}

run_initializer
test "$(rg -c '^topics .* --create ' "$scratch_dir/kafka.log")" -eq 7
test "$(rg -c '^topics .* --describe ' "$scratch_dir/kafka.log")" -eq 7
test "$(rg -c '^configs .* --alter ' "$scratch_dir/kafka.log")" -eq 7
test "$(rg -c 'retention.ms=7776000000' "$scratch_dir/kafka.log")" -eq 2
test "$(rg -c 'auction.runtime.projection.v1.*--partitions 24' "$scratch_dir/kafka.log")" -eq 1
test "$(rg -c 'auction.dlq.v1.*--partitions 3' "$scratch_dir/kafka.log")" -eq 1

: >"$scratch_dir/kafka.log"
if KAFKA_FAKE_MODE=undersized run_initializer >"$scratch_dir/undersized.log" 2>&1; then
  echo "initializer must reject an undersized topic because expansion remaps active keys" >&2
  exit 1
fi
rg -q 'expected immutable count' "$scratch_dir/undersized.log"
rg -q 'KAFKA_PARTITION_MIGRATION_RUNBOOK.md' "$scratch_dir/undersized.log"
if rg -q '^topics .* --alter ' "$scratch_dir/kafka.log"; then
  echo "initializer must never alter a topic partition count" >&2
  exit 1
fi

if KAFKA_FAKE_MODE=oversized run_initializer 2>/dev/null; then
  echo "initializer must reject an oversized topic because partition count is immutable" >&2
  exit 1
fi
if KAFKA_REPLICATION_FACTOR=0 run_initializer 2>/dev/null; then
  echo "initializer must reject a zero replication factor" >&2
  exit 1
fi
if KAFKA_REPLICATION_FACTOR=1 KAFKA_MIN_INSYNC_REPLICAS=2 run_initializer 2>/dev/null; then
  echo "initializer must reject min ISR greater than the replication factor" >&2
  exit 1
fi
if KAFKA_REPLICATION_FACTOR=3 KAFKA_MIN_INSYNC_REPLICAS=2 KAFKA_FAKE_ACTUAL_REPLICATION_FACTOR=1 run_initializer >"$scratch_dir/replication-drift.log" 2>&1; then
  echo "initializer must reject an existing topic with the wrong replication factor" >&2
  exit 1
fi
rg -q 'refusing implicit replica reassignment' "$scratch_dir/replication-drift.log"

rg -q '禁止.*Runtime Topic.*直接.*--alter --partitions' "$repo_root/docs/KAFKA_PARTITION_MIGRATION_RUNBOOK.md"
rg -q '无法回滚.*缩容' "$repo_root/docs/KAFKA_PARTITION_MIGRATION_RUNBOOK.md"
