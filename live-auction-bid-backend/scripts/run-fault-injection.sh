#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
compose_file="${COMPOSE_FILE:-$repo_dir/deploy/docker-compose.yml}"
scenario="${1:---list}"
duration_seconds="${FAULT_DURATION_SECONDS:-10}"
recovery_timeout_seconds="${FAULT_RECOVERY_TIMEOUT_SECONDS:-90}"
assertion_timeout_seconds="${FAULT_ASSERTION_TIMEOUT_SECONDS:-120}"
assertion_script="${FAULT_ASSERTION_SCRIPT:-}"
assert_after_service_start="${FAULT_ASSERT_AFTER_SERVICE_START:-0}"
run_id="${RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
results_root="${RESULTS_DIR:-$repo_dir/test-results/fault-injection}"
result_dir="$results_root/$run_id-$scenario"
fault_active=0
container_id=""
mode=""
service=""
assertion_phase="not_started"

usage() {
  cat <<'EOF'
Usage: CONFIRM_FAULT_INJECTION=1 ./scripts/run-fault-injection.sh <scenario>

Scenarios:
  redis-pause       Pause Redis, then recover it after FAULT_DURATION_SECONDS.
  redis-kill        SIGKILL Redis, then recreate the exact Compose service.
  mysql-pause       Pause MySQL, then recover it after FAULT_DURATION_SECONDS.
  mysql-kill        SIGKILL MySQL, then recreate the exact Compose service.
  gateway-pause     Pause the Gateway process, then recover it.
  gateway-kill      SIGKILL the Gateway, then recreate it.
  auction-pause     Pause auction-service without enabling a MySQL fallback.
  auction-kill      SIGKILL auction-service, then recreate it.
  relay-pause       Pause outbox-relay to accumulate Redis outbox backlog.
  relay-kill        SIGKILL outbox-relay, then verify the service recovers.
  projector-pause   Pause Projector to accumulate Kafka lag.
  projector-kill    SIGKILL Projector, then verify DB-offset recovery starts.
  domain-pause      Pause domain-relay to accumulate MySQL domain outbox rows.
  domain-kill       SIGKILL domain-relay, then verify lease-safe recovery.
  enrichment-pause Pause enrichment-consumer to accumulate its Kafka lag.
  enrichment-kill  SIGKILL enrichment-consumer, then verify idempotent recovery.
  index-es-pause    Pause the Elasticsearch index consumer.
  index-es-kill     SIGKILL the Elasticsearch index consumer, then recover it.
  index-pgvector-pause
                    Pause the pgvector index consumer.
  index-pgvector-kill
                    SIGKILL the pgvector index consumer, then recover it.
  close-pause       Pause close-worker while expired lots accumulate.
  close-kill        SIGKILL close-worker, then verify deadline-safe recovery.
  nats-pause        Pause NATS to exercise realtime and best-effort fanout degradation.
  nats-kill         SIGKILL NATS, then recreate the exact Compose service.
  elasticsearch-pause
                    Pause Elasticsearch to exercise search degradation and index backlog.
  elasticsearch-kill
                    SIGKILL Elasticsearch, then recreate the exact Compose service.
  kafka-pause       Pause the current Kafka broker, then recover it.
  kafka-kill        SIGKILL Kafka, then recreate the exact Compose service.

Set DRY_RUN=1 to resolve and print the exact target without mutating containers.
Every mutating run requires FAULT_ASSERTION_SCRIPT=/absolute/path/to/executable.
The assertion executable is called with phases before, during, and after and
must fail when the scenario's business invariant is not satisfied. Process
recovery alone is never reported as a successful chaos result.
Results are archived below test-results/fault-injection/ by default.
EOF
}

case "$scenario" in
  --list|-h|--help)
    usage
    exit 0
    ;;
  redis-pause) service="redis"; mode="pause" ;;
  redis-kill) service="redis"; mode="kill" ;;
  mysql-pause) service="mysql"; mode="pause" ;;
  mysql-kill) service="mysql"; mode="kill" ;;
  gateway-pause) service="gateway"; mode="pause" ;;
  gateway-kill) service="gateway"; mode="kill" ;;
  auction-pause) service="auction-service"; mode="pause" ;;
  auction-kill) service="auction-service"; mode="kill" ;;
  relay-pause) service="outbox-relay"; mode="pause" ;;
  relay-kill) service="outbox-relay"; mode="kill" ;;
  projector-pause) service="projector"; mode="pause" ;;
  projector-kill) service="projector"; mode="kill" ;;
  domain-pause) service="domain-relay"; mode="pause" ;;
  domain-kill) service="domain-relay"; mode="kill" ;;
  enrichment-pause) service="enrichment-consumer"; mode="pause" ;;
  enrichment-kill) service="enrichment-consumer"; mode="kill" ;;
  index-es-pause) service="index-es"; mode="pause" ;;
  index-es-kill) service="index-es"; mode="kill" ;;
  index-pgvector-pause) service="index-pgvector"; mode="pause" ;;
  index-pgvector-kill) service="index-pgvector"; mode="kill" ;;
  close-pause) service="close-worker"; mode="pause" ;;
  close-kill) service="close-worker"; mode="kill" ;;
  nats-pause) service="nats"; mode="pause" ;;
  nats-kill) service="nats"; mode="kill" ;;
  elasticsearch-pause) service="elasticsearch"; mode="pause" ;;
  elasticsearch-kill) service="elasticsearch"; mode="kill" ;;
  kafka-pause) service="kafka"; mode="pause" ;;
  kafka-kill) service="kafka"; mode="kill" ;;
  *)
    echo "Unknown fault-injection scenario: $scenario" >&2
    usage >&2
    exit 2
    ;;
esac

if [[ ! "$duration_seconds" =~ ^[1-9][0-9]*$ ]] || (( duration_seconds > 300 )); then
  echo "FAULT_DURATION_SECONDS must be an integer from 1 to 300" >&2
  exit 2
fi
if [[ ! "$recovery_timeout_seconds" =~ ^[1-9][0-9]*$ ]] || (( recovery_timeout_seconds > 900 )); then
  echo "FAULT_RECOVERY_TIMEOUT_SECONDS must be an integer from 1 to 900" >&2
  exit 2
fi
if [[ ! "$assertion_timeout_seconds" =~ ^[1-9][0-9]*$ ]] || (( assertion_timeout_seconds > 1800 )); then
  echo "FAULT_ASSERTION_TIMEOUT_SECONDS must be an integer from 1 to 1800" >&2
  exit 2
fi
if [[ "$assert_after_service_start" != "0" && "$assert_after_service_start" != "1" ]]; then
  echo "FAULT_ASSERT_AFTER_SERVICE_START must be 0 or 1" >&2
  exit 2
fi
if [[ ! -f "$compose_file" ]]; then
  echo "Compose file not found: $compose_file" >&2
  exit 2
fi
if [[ "${DRY_RUN:-0}" != "1" ]]; then
  if [[ -z "$assertion_script" ]]; then
    echo "Refusing process-only fault injection: FAULT_ASSERTION_SCRIPT is required" >&2
    exit 2
  fi
  if [[ "$assertion_script" != /* || ! -f "$assertion_script" || ! -x "$assertion_script" ]]; then
    echo "FAULT_ASSERTION_SCRIPT must be an absolute executable file: $assertion_script" >&2
    exit 2
  fi
  if ! command -v timeout >/dev/null 2>&1; then
    echo "timeout is required to bound fault assertion execution" >&2
    exit 2
  fi
fi

compose=(docker compose -f "$compose_file")
if ! "${compose[@]}" config --services | grep -Fxq "$service"; then
  echo "Compose service '$service' is not installed in $compose_file" >&2
  exit 2
fi
container_id="$("${compose[@]}" ps -q "$service")"
if [[ -z "$container_id" ]]; then
  echo "Compose service '$service' is not running" >&2
  exit 2
fi

printf 'scenario=%s mode=%s service=%s container_id=%s duration=%ss\n' \
  "$scenario" "$mode" "$service" "$container_id" "$duration_seconds"
if [[ "${DRY_RUN:-0}" == "1" ]]; then
  exit 0
fi
if [[ "${CONFIRM_FAULT_INJECTION:-0}" != "1" ]]; then
  echo "Refusing to inject a fault without CONFIRM_FAULT_INJECTION=1" >&2
  exit 2
fi

mkdir -p "$result_dir"
"${compose[@]}" ps >"$result_dir/compose-before.txt"
docker inspect "$container_id" >"$result_dir/target-before.json"

run_assertion() {
  local phase="$1"
  local log_file="$result_dir/assertion-$phase.log"
  local exit_code=0
  local -a pipeline_status=()
  assertion_phase="$phase"
  set +e
  FAULT_SCENARIO="$scenario" \
    FAULT_PHASE="$phase" \
    FAULT_SERVICE="$service" \
    FAULT_CONTAINER_ID="$container_id" \
    FAULT_RESULT_DIR="$result_dir" \
    FAULT_COMPOSE_FILE="$compose_file" \
    timeout --signal=TERM "${assertion_timeout_seconds}s" \
    "$assertion_script" "$phase" "$scenario" "$result_dir" "$service" "$container_id" \
    2>&1 | tee "$log_file"
  pipeline_status=("${PIPESTATUS[@]}")
  exit_code="${pipeline_status[0]}"
  if (( pipeline_status[1] != 0 )); then
    exit_code=74
  fi
  set -e
  if (( exit_code != 0 )); then
    printf 'status=FAIL\nfailed_phase=%s\nassertion_exit_code=%s\ncompleted_at=%s\n' \
      "$phase" "$exit_code" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$result_dir/result.txt"
    return "$exit_code"
  fi
}

recover() {
  if (( fault_active == 0 )); then
    return
  fi
  recover_service
}
trap recover EXIT INT TERM

recover_service() {
  if [[ "$mode" == "pause" ]]; then
    docker unpause "$container_id" >/dev/null 2>&1 || true
    return
  fi
  if "${compose[@]}" start "$service" >/dev/null 2>&1; then
    return
  fi
  "${compose[@]}" up -d --no-deps "$service" >/dev/null 2>&1 || true
}

{
  printf 'assertion_script=%s\n' "$assertion_script"
  printf 'assertion_timeout_seconds=%s\n' "$assertion_timeout_seconds"
  printf 'recovery_timeout_seconds=%s\n' "$recovery_timeout_seconds"
  printf 'assert_after_service_start=%s\n' "$assert_after_service_start"
  sha256sum "$assertion_script"
} >"$result_dir/assertion-contract.txt"

run_assertion before

if [[ "$mode" == "pause" ]]; then
  docker pause "$container_id" >/dev/null
  fault_active=1
  fault_started_at="$SECONDS"
  run_assertion during
  elapsed_seconds=$((SECONDS - fault_started_at))
  if (( elapsed_seconds < duration_seconds )); then
    sleep "$((duration_seconds - elapsed_seconds))"
  fi
  docker unpause "$container_id" >/dev/null
else
  docker kill --signal KILL "$container_id" >/dev/null
  fault_active=1
  run_assertion during
  recover_service
fi
fault_active=0

deadline=$((SECONDS + recovery_timeout_seconds))
state="unknown"
if [[ "$assert_after_service_start" == "1" ]]; then
  while (( SECONDS < deadline )); do
    container_id="$("${compose[@]}" ps -q "$service")"
    state="$(docker inspect --format '{{.State.Status}}' "$container_id" 2>/dev/null || true)"
    if [[ "$state" == "running" ]]; then
      break
    fi
    sleep 1
  done
  if [[ "$state" != "running" ]]; then
    printf 'status=FAIL\nfailed_phase=recovery_start\nrecovered_state=%s\ncompleted_at=%s\n' \
      "$state" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$result_dir/result.txt"
    echo "Service '$service' did not start within $recovery_timeout_seconds seconds; state=$state" >&2
    exit 1
  fi
  run_assertion after
fi

while true; do
  container_id="$("${compose[@]}" ps -q "$service")"
  state="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}no-health:{{.State.Status}}{{end}}' "$container_id" 2>/dev/null || true)"
  if [[ "$state" == "healthy" || "$state" == "no-health:running" ]]; then
    break
  fi
  if (( SECONDS >= deadline )); then
    break
  fi
  sleep 1
done
if [[ "$state" != "healthy" && "$state" != "no-health:running" ]]; then
  printf 'status=FAIL\nfailed_phase=recovery\nrecovered_state=%s\ncompleted_at=%s\n' \
    "$state" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$result_dir/result.txt"
  echo "Service '$service' did not recover within $recovery_timeout_seconds seconds; state=$state" >&2
  exit 1
fi

if [[ "$assert_after_service_start" != "1" ]]; then
  run_assertion after
fi

"${compose[@]}" ps >"$result_dir/compose-after.txt"
docker inspect "$container_id" >"$result_dir/target-after.json"
"${compose[@]}" logs --no-color --tail 200 "$service" >"$result_dir/service.log" 2>&1 || true
printf 'status=PASS\nassertion_phase=%s\nrecovered_state=%s\ncompleted_at=%s\n' \
  "$assertion_phase" "$state" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$result_dir/result.txt"
echo "Fault injection completed: $result_dir"
