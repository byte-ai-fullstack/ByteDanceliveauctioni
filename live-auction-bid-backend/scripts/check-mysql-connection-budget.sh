#!/usr/bin/env bash
set -euo pipefail

mysql_max_connections="${MYSQL_MAX_CONNECTIONS:-600}"
mysql_reserved_percent="${MYSQL_RESERVED_PERCENT:-20}"
gateway_replicas="${GATEWAY_REPLICAS:-6}"
gateway_max_open="${GATEWAY_MAX_OPEN:-25}"
auction_replicas="${AUCTION_SERVICE_REPLICAS:-12}"
auction_max_open="${AUCTION_SERVICE_MAX_OPEN:-10}"
projector_replicas="${PROJECTOR_REPLICAS:-24}"
projector_max_open="${PROJECTOR_MAX_OPEN:-5}"
worker_replicas="${DOMAIN_WORKER_REPLICAS:-8}"
worker_max_open="${DOMAIN_WORKER_MAX_OPEN:-5}"

for value in \
  "$mysql_max_connections" "$gateway_replicas" "$gateway_max_open" \
  "$auction_replicas" "$auction_max_open" "$projector_replicas" \
  "$projector_max_open" "$worker_replicas" "$worker_max_open"; do
  if ! [[ "$value" =~ ^[1-9][0-9]*$ ]]; then
    echo "connection budget inputs must be positive integers" >&2
    exit 2
  fi
done
if ! [[ "$mysql_reserved_percent" =~ ^[0-9]+$ ]] || ((mysql_reserved_percent < 10 || mysql_reserved_percent > 50)); then
  echo "MYSQL_RESERVED_PERCENT must be an integer from 10 through 50" >&2
  exit 2
fi

gateway_connections=$((gateway_replicas * gateway_max_open))
auction_connections=$((auction_replicas * auction_max_open))
projector_connections=$((projector_replicas * projector_max_open))
worker_connections=$((worker_replicas * worker_max_open))
requested_connections=$((gateway_connections + auction_connections + projector_connections + worker_connections))
usable_connections=$((mysql_max_connections * (100 - mysql_reserved_percent) / 100))

printf 'mysql_max=%d reserved_percent=%d usable=%d requested=%d gateway=%d auction=%d projector=%d workers=%d\n' \
  "$mysql_max_connections" "$mysql_reserved_percent" "$usable_connections" "$requested_connections" \
  "$gateway_connections" "$auction_connections" "$projector_connections" "$worker_connections"

if ((requested_connections > usable_connections)); then
  echo "MySQL connection budget exceeded by $((requested_connections - usable_connections)) connections" >&2
  exit 1
fi
