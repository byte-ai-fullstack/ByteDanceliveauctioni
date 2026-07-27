#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
drill="$repo_root/scripts/run-l2-ha-fault-drill.sh"

bash -n "$drill"
rg -q 'down --volumes --remove-orphans' "$drill"
rg -q '^produce_marker\(\)' "$drill"
rg -q -- '--sync' "$drill"
rg -q -- '--producer-property acks=all' "$drill"
rg -q 'produce_marker kafka-1 baseline' "$drill"
rg -q 'produce_marker kafka-2 one-broker-down' "$drill"
rg -q 'if produce_marker kafka-3 two-brokers-down' "$drill"
rg -q '^assert_kafka_markers\(\)' "$drill"
rg -q 'Kafka failover did not retain both acknowledged drill records' "$drill"
rg -q '^wait_for_redis_replica\(\)' "$drill"
rg -q "role:slave" "$drill"
rg -q "master_link_status:up" "$drill"
rg -q 'wait_for_redis_replica "\$old_master"' "$drill"

echo "HA drill proves Kafka acknowledged-record retention and Redis demoted-primary rejoin semantics."
