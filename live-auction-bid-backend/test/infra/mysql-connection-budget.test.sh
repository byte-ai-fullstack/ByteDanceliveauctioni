#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
checker="$repo_root/scripts/check-mysql-connection-budget.sh"

output="$($checker)"
[[ "$output" == *"usable=480"* ]]
[[ "$output" == *"requested=430"* ]]

if MYSQL_MAX_CONNECTIONS=100 "$checker" >/dev/null 2>&1; then
  echo "connection budget must reject an oversubscribed database" >&2
  exit 1
fi
if MYSQL_RESERVED_PERCENT=5 "$checker" >/dev/null 2>&1; then
  echo "connection budget must reject an unsafe operations reserve" >&2
  exit 1
fi
if PROJECTOR_REPLICAS=none "$checker" >/dev/null 2>&1; then
  echo "connection budget must reject non-integer inputs" >&2
  exit 1
fi
