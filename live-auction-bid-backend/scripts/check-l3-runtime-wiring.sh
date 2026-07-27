#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

legacy_wiring='NewEventOutboxWorker|NewRuntimeProjectionWorker|NewAuctionCloseWorker|SetSyncRuntimeProjection|NewRedisStreamBus|redis_stream'
if rg -n "$legacy_wiring" app/auction/service/cmd; then
  echo "Legacy MySQL/Redis Stream runtime wiring is forbidden in production commands." >&2
  exit 1
fi

legacy_config='AUCTION_BID_SYNC_PROJECTION|AUCTION_RUNTIME_PROJECT_LEGACY_STREAMS|AUCTION_RUNTIME_PROJECTION_(SHARDS|BACKPRESSURE|BATCH_LIMIT|DRAIN_BATCHES|INTERVAL)|AUCTION_REALTIME_BUS_(STREAM|GROUP|CONSUMER)'
if rg -n "$legacy_config" deploy --glob '!deploy/.env'; then
  echo "Legacy runtime configuration is forbidden in tracked deployment files." >&2
  exit 1
fi

if [[ -e app/auction/service/cmd/reconcile_runtime_lot/main.go ]]; then
  echo "The unsafe manual MySQL-to-Redis runtime overwrite command must stay deleted." >&2
  exit 1
fi

for required in RuntimeGenerationGuardEnabled StartRuntimeGenerationGuard RunSentinelSwitchMasterWatcher; do
  if ! rg -q "$required" app/auction/service/cmd/auction-service/main.go; then
    echo "Missing runtime generation safety wiring: $required" >&2
    exit 1
  fi
  if rg -q "$required" app/auction/service/cmd/server/main.go; then
    echo "Gateway must not own runtime generation safety wiring: $required" >&2
    exit 1
  fi
done

for required in projectiongate.NewKafkaSource projectiongate.NewSQLSource projectiongate.NewGuard SetRuntimeAdmissionGate; do
  if ! rg -q "$required" app/auction/service/cmd/auction-service/main.go; then
    echo "Missing end-to-end projection gate wiring: $required" >&2
    exit 1
  fi
done
if [[ "$(rg -c 's\.checkRuntimeAdmission\(ctx\)' app/auction/service/internal/data/runtime_command_store.go)" -ne 3 ]]; then
  echo "Projection admission must guard exactly start, bid and live config sync commands." >&2
  exit 1
fi
if ! rg -q 'HandleFunc\("/admissionz"' app/auction/service/internal/server/operation.go; then
  echo "Projection admission must expose its independent operations endpoint." >&2
  exit 1
fi
if rg -n 'httpGet: \{path: /admissionz' deploy/kubernetes; then
  echo "Projection admission must not be used as a Kubernetes readiness or liveness probe." >&2
  exit 1
fi
rg -q -- 'AUCTION_PROJECTION_GATE_ENABLED: "true"' deploy/docker-compose.yml
rg -q -- 'AUCTION_PROJECTION_GATE_ENABLED: "true"' deploy/prod/docker-compose.yml
rg -q -- 'AUCTION_PROJECTION_GATE_ENABLED: "true"' deploy/kubernetes/base/runtime-config.yaml

for script in start_lot.lua place_bid.lua cancel_lot.lua close_if_expired.lua sync_lot_config.lua; do
  if ! rg -q "LOT_FROZEN" "app/auction/service/internal/data/lua/$script"; then
    echo "Runtime Lua script is missing its per-lot reconciliation fence: $script" >&2
    exit 1
  fi
done

if rg -n 'AUCTION_OUTBOX_SHARDS:' deploy --glob '!deploy/.env' \
    | rg -v 'AUCTION_OUTBOX_SHARDS:[[:space:]]*"16"[[:space:]]*$'; then
  echo "Tracked deployment files must pin the immutable Runtime Outbox topology to 16 shards." >&2
  exit 1
fi

echo "L3 production runtime wiring contains no legacy worker or Redis Stream enablement."
