#!/usr/bin/env bash
set -euo pipefail

printf 'topics %s\n' "$*" >>"$KAFKA_FAKE_LOG"

topic=""
previous=""
for argument in "$@"; do
  if [[ "$previous" == "--topic" ]]; then
    topic="$argument"
    break
  fi
  previous="$argument"
done

if [[ " $* " != *" --describe "* ]]; then
  exit 0
fi

case "$topic" in
  auction.runtime.projection.v1 | auction.bid.accepted.v1) partitions=24 ;;
  auction.lot.settled.v1 | auction.order.created.v1 | auction.lot.state.v1 | auction.order.enrichment.requested.v1) partitions=12 ;;
  auction.dlq.v1) partitions=3 ;;
  *) echo "unexpected topic $topic" >&2; exit 1 ;;
esac

case "${KAFKA_FAKE_MODE:-expected}" in
  expected) ;;
  undersized) ((partitions--)) ;;
  oversized) ((partitions++)) ;;
  *) echo "unknown KAFKA_FAKE_MODE" >&2; exit 1 ;;
esac

replication_factor="${KAFKA_FAKE_ACTUAL_REPLICATION_FACTOR:-${KAFKA_REPLICATION_FACTOR:-1}}"
printf 'Topic: %s\tPartitionCount: %d\tReplicationFactor: %d\n' "$topic" "$partitions" "$replication_factor"
