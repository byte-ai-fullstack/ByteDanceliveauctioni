#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

fail=0
roots=(app api deploy scripts)
documentation_roots=(docs README.md)
if [[ -d .github ]]; then
  documentation_roots+=(.github)
fi

check() {
  local description="$1"
  local pattern="$2"
  shift 2
  if rg -n -i \
      --glob '!deploy/.env' \
      --glob '!**/vendor/**' \
      --glob '!scripts/check-zero-residue.sh' \
      --glob '!scripts/check-l3-runtime-wiring.sh' \
      -- "$pattern" "$@"; then
    echo "ERROR: residue found: $description" >&2
    fail=1
  fi
}

require() {
  local description="$1"
  local pattern="$2"
  shift 2
  if ! rg -q -- "$pattern" "$@"; then
    echo "ERROR: required invariant missing: $description" >&2
    fail=1
  fi
}

# Match concrete go-redis method calls and Lua commands, not domain names such
# as RuntimeOutboxAckResult whose characters happen to contain "xAck".
check "obsolete Redis log commands" \
  "\\.X(Add|ReadGroup|Ack|AutoClaim|GroupCreate|Trim|Range)\\b|redis\\.call\\([[:space:]]*['\"]X(ADD|READGROUP|ACK|AUTOCLAIM|GROUP|TRIM|RANGE)" "${roots[@]}"
check "obsolete runtime projection worker" \
  'RuntimeProjectionWorker|RuntimeProjectionShardOffset|runtime_projection_shard_offsets' "${roots[@]}"
check "obsolete delivery fields" \
  'RuntimeStreamID|LastStreamID|StreamedAtUnixMs|LastStreamError|last_stream_id|streamed_at_unix_ms|last_stream_error' "${roots[@]}"
check "obsolete realtime implementation" \
  'RedisStreamBus|NewRedisStreamBus|redis_stream' "${roots[@]}"
check "obsolete configuration" \
  'AUCTION_RUNTIME_PROJECT_LEGACY_STREAMS|AUCTION_RUNTIME_PROJECTION_SHARDS|AUCTION_REALTIME_BUS_STREAM|AUCTION_REALTIME_BUS_GROUP|AUCTION_REALTIME_BUS_CONSUMER' "${roots[@]}"
check "obsolete metrics" \
  'runtime_event_xadd|auction_runtime_event_xadd_total' "${roots[@]}"
check "obsolete static shard control plane" \
  'AUCTION_CLUSTER_REGISTRY_JSON|AUCTION_CLUSTER_MODE|auction-shard-gateway|EvaluateScale|internal/cluster' "${roots[@]}"
check "obsolete combined gateway/auction-service NATS principal" \
  'NATS_MONOLITH_PASSWORD|user:[[:space:]]*monolith|combined HTTP/WS process' deploy scripts
check "obsolete Consul discovery" \
  'AUCTION_CONSUL_|RegisterConsul|ConsulRegistration|hashicorp/consul' "${roots[@]}"
check "obsolete discovery documentation" \
  '\bConsul\b|AUCTION_CLUSTER_REGISTRY_JSON|auction-shard-gateway|cluster/gateway|/clusterz|/workerz|auction-service[^[:cntrl:]]*StatefulSet|StatefulSet[^[:cntrl:]]*auction-service' \
  "${documentation_roots[@]}"
check "obsolete event-log documentation" \
  'Redis[ _-]*Stream|XADD|XREADGROUP|XACK|XAUTOCLAIM|XGROUP|XTRIM|XRANGE|RuntimeStreamID|LastStreamID|streamed_at_unix_ms|last_stream_error|AUCTION_RUNTIME_PROJECT_LEGACY_STREAMS|AUCTION_REALTIME_BUS_STREAM' \
  "${documentation_roots[@]}"
check "application-owned MySQL AutoMigrate" \
  'AutoMigrate' app/auction/service/internal/data app/auction/service/cmd
check "optional gateway command routing" \
  'AUCTION_COMMAND_GRPC_REQUIRED|localCommandMode' app deploy scripts
check "obsolete MySQL runtime adjudication fallback" \
  'StartLotAsOnlyActive|CommitAcceptedBid|CreateOrderForSettledLot|CloseExpiredLots|CloseExpiredOpenLots|CloseStalePreStartLots|HydrateLotRuntime|SyncLotRuntime|CancelLotRuntime|ExpiredLotRepository|RedisLeaseProvider|type[[:space:]]+AuctionCloseWorker' \
  "${roots[@]}"
check "obsolete MySQL bid-status guard" \
  'AUCTION_BID_DB_GUARD_MODE|SetBidDBGuardMode|bidDBGuardMode|readBidGuardLot|closedLotBidRejectError' \
  "${roots[@]}"
check "obsolete MySQL expiry and stale pre-start decisions" \
  'ListExpiredOpen|ListStalePreStart|IsStalePreStartLot|IsAutoCancellablePreStartStatus|FailExpiredLot' \
  "${roots[@]}"
check "obsolete generic worker lease metric" \
  'auction_worker_lease_active|SetWorkerLeaseActive' "${roots[@]}"
check "presentation actions advancing the runtime aggregate through the removed save path" \
  'uc\.lots\.Save\(ctx, lot, expectedVersion, \[\]\*v1\.AuctionEvent\{event\}\)' \
  app/auction/service/internal/biz/auction/usecase.go
check "obsolete pure-Go runtime adjudication entry points" \
  '^func[[:space:]]+(StartLot|AcceptBid|SettleLot|CancelLot)\(lot[[:space:]]+\*v1\.Lot' \
  app/auction/service/internal/biz/auction/lot.go
check "query-side repair of projected room runtime pointers" \
  'RepairRoomActiveLot|FindOrCreateRoomState|clearStaleOrRejectActiveLot' \
  app/auction/service/internal

require "presentation actions use an independent repository" \
  'SaveLotPresentation' app/auction/service/internal/biz/auction/usecase.go
require "gateway routes trust reveal through auction-service" \
  'func \(p \*AuctionProxy\) RevealTrustCard' app/auction/service/internal/gateway/auction_proxy.go
require "gateway routes duel start through auction-service" \
  'func \(p \*AuctionProxy\) StartDuel' app/auction/service/internal/gateway/auction_proxy.go
require "presentation version advances independently from lot.version" \
  'PresentationVersion\+\+' app/auction/service/internal/biz/auction/lot.go
require "target schema owns the presentation aggregate" \
  'CREATE TABLE auction_lot_presentations' deploy/mysql/migrations/000005_lot_presentation.up.sql
require "generic lot save rejects runtime lifecycle writes" \
  'generic lot save only supports pre-start configuration' app/auction/service/internal/data/lot_repo.go
require "domain relay has a dedicated publish-only NATS principal" \
  'user:[[:space:]]*domain-relay' deploy/nats/local.conf deploy/nats/ha.conf
require "domain relay wires post-publication order READY acceleration" \
  'WithOrderReadyPublisher' app/auction/service/cmd/domain-relay/main.go
require "production injects the dedicated domain relay NATS URL" \
  'AUCTION_DOMAIN_RELAY_NATS_URLS' deploy/prod/docker-compose.yml deploy/kubernetes/base/workloads.yaml
require "golangci enforces the domain dependency boundary" \
  'domain-boundary' .golangci.yml
require "the domain dependency boundary rejects ORM imports" \
  'gorm\.io/gorm' .golangci.yml
require "the domain dependency boundary rejects Redis clients" \
  'github\.com/redis/go-redis' .golangci.yml
require "the domain dependency boundary rejects Kafka clients" \
  'github\.com/twmb/franz-go' .golangci.yml
require "draft edits advance the start configuration fence" \
  'candidate\.ConfigVersion\+\+' app/auction/service/internal/biz/auction/lot.go
require "runtime start locks the current MySQL configuration row" \
  'Holding.*row lock|Clauses\(clause\.Locking\{Strength: "UPDATE"\}\)' app/auction/service/internal/data/runtime_command_store.go
require "draft save checks that Redis runtime has not started" \
  'ensureRuntimeStateAbsentForPreStartUpdate' app/auction/service/internal/data/lot_repo.go
require "queue transition checks that Redis runtime has not started" \
  'ensureRuntimeStateAbsentForPreStartUpdate\(ctx, lotID\)' app/auction/service/internal/data/room_state_repo.go
require "Redis-only close worker cannot expose a start path" \
  'runtime start requires the MySQL-backed auction service store' app/auction/service/internal/data/runtime_close_store.go
require "snapshot room projection lookup is read-only" \
  'func \(s \*Store\) FindRoomState' app/auction/service/internal/data/room_state_repo.go
require "bid adjudication goes directly to the Redis runtime" \
  'result, err := uc\.runtime\.PlaceBidRuntime\(ctx, lot, req, bidderID, nickname, avatarURL, bidID, nowMs\)' \
  app/auction/service/internal/biz/auction/usecase.go
require "Redis classifies cancelled-lot bid rejection without MySQL" \
  "previous_status == cancelled_status" \
  app/auction/service/internal/data/lua/place_bid.lua
require "Redis exposes the cancelled-lot business code" \
  "reject\('LOT_CANCELLED'" \
  app/auction/service/internal/data/lua/place_bid.lua
require "failover reconciliation includes MySQL active projections" \
  'addMySQLActiveLotIDs\(ctx, seen\)' \
  app/auction/service/internal/data/runtime_reconciler.go
require "domain relay blocks a later same-route outbox row behind its unpublished predecessor" \
  'predecessor\.topic = candidate\.topic' \
  app/auction/service/internal/worker/domainrelay/store.go
require "domain relay serializes claimed messages within one Kafka route" \
  'same_route_followers_blocked' \
  app/auction/service/internal/worker/domainrelay/relay.go
require "committed business writes treat Core NATS fanout as recoverable best effort" \
  'broadcastCommittedBestEffort' \
  app/auction/service/internal/biz/auction/usecase.go

obsolete_files=(
  app/auction/service/internal/data/runtime_projection_worker.go
  app/auction/service/internal/data/runtime_projection_shard_repo.go
  app/auction/service/internal/data/event_outbox_worker.go
  app/auction/service/internal/biz/auction/runtime_projection.go
  app/auction/service/internal/realtime/redis_bus.go
  app/auction/service/internal/realtime/redis_stream_bus.go
  app/auction/service/cmd/shard_gateway
  app/auction/service/internal/cluster
  app/auction/service/internal/server/registry.go
  app/auction/service/internal/biz/auction/close_worker.go
  app/auction/service/internal/biz/auction/lease.go
  app/auction/service/internal/biz/auction/worker_helpers.go
  app/auction/service/internal/data/lease.go
  deploy/prod/docker-compose.shard-stack.yml
  deploy/prod/docker-compose.gateway.yml
  deploy/mysql/migrations/20260531_runtime_projection_offsets.sql
  deploy/mysql/migrations/20260531_runtime_projection_shard_offsets.sql
  deploy/mysql/migrations/20260601_fix_runtime_projection_shard_zero.sql
)
for path in "${obsolete_files[@]}"; do
  if [[ -e "$path" ]]; then
    echo "ERROR: obsolete file still exists: $path" >&2
    fail=1
  fi
done

if (( fail != 0 )); then
  exit "$fail"
fi
echo "Runtime, presentation, deployment, scripts, and current documentation satisfy the zero-residue invariants."
