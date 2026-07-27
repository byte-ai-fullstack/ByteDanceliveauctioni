#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
runner="$repo_root/scripts/run-fault-injection.sh"
scratch_dir="$(mktemp -d)"
trap 'rm -rf -- "$scratch_dir"' EXIT

mkdir -p "$scratch_dir/bin" "$scratch_dir/results"

cat >"$scratch_dir/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"$FAULT_TEST_DOCKER_LOG"
case "$*" in
  "compose -f "*" config --services")
    printf '%s\n' redis mysql gateway auction-service outbox-relay projector domain-relay enrichment-consumer index-es index-pgvector close-worker nats elasticsearch kafka
    ;;
  "compose -f "*" ps -q "*)
    printf 'container-test-id\n'
    ;;
  "compose -f "*" ps")
    printf 'NAME STATUS\noutbox-relay running\n'
    ;;
  "compose -f "*" logs "*)
    printf 'mock service log\n'
    ;;
  "compose -f "*" start "*)
    if [[ "$*" == *" start ${FAULT_TEST_FAIL_START_SERVICE:-__never__}" && ! -f "${FAULT_TEST_START_FAIL_SENTINEL:-}" ]]; then
      touch "${FAULT_TEST_START_FAIL_SENTINEL:-/tmp/fault-test-start-sentinel}"
      exit 1
    fi
    ;;
  "compose -f "*" up -d --no-deps "*)
    ;;
  "inspect --format {{.State.Status}} "*)
    printf 'running\n'
    ;;
  "inspect --format {{if .State.Health}}{{.State.Health.Status}}{{else}}no-health:{{.State.Status}}{{end}} "*)
    printf '%s\n' "${FAULT_TEST_HEALTH_STATE:-healthy}"
    ;;
  "inspect --format "*)
    printf 'healthy\n'
    ;;
  "inspect "*)
    printf '{"State":{"Status":"running"}}\n'
    ;;
  "kill --signal KILL "*|"pause "*|"unpause "*)
    ;;
  *)
    printf 'unexpected docker invocation: %s\n' "$*" >&2
    exit 64
    ;;
esac
EOF
chmod +x "$scratch_dir/bin/docker"

cat >"$scratch_dir/assert.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s %s %s %s %s %s\n' \
  "$FAULT_PHASE" "$FAULT_SCENARIO" "$FAULT_SERVICE" \
  "$1" "$2" "$4" >>"$FAULT_TEST_ASSERT_LOG"
test "$FAULT_PHASE" = "$1"
test "$FAULT_SCENARIO" = "$2"
test "$FAULT_RESULT_DIR" = "$3"
test "$FAULT_SERVICE" = "$4"
test "$FAULT_CONTAINER_ID" = "$5"
if [[ "${FAULT_TEST_FAIL_PHASE:-}" == "$FAULT_PHASE" ]]; then
  exit 23
fi
if [[ "$FAULT_PHASE" == "after" && -n "${FAULT_TEST_SLEEP_AFTER_SECONDS:-}" ]]; then
  sleep "$FAULT_TEST_SLEEP_AFTER_SECONDS"
fi
EOF
chmod +x "$scratch_dir/assert.sh"

export PATH="$scratch_dir/bin:$PATH"
export FAULT_TEST_DOCKER_LOG="$scratch_dir/docker.log"
export FAULT_TEST_ASSERT_LOG="$scratch_dir/assert.log"

DRY_RUN=1 "$runner" relay-kill >"$scratch_dir/dry-run.log"
rg -q 'scenario=relay-kill mode=kill service=outbox-relay' "$scratch_dir/dry-run.log"
DRY_RUN=1 "$runner" nats-pause >"$scratch_dir/dry-run-nats.log"
rg -q 'scenario=nats-pause mode=pause service=nats' "$scratch_dir/dry-run-nats.log"
DRY_RUN=1 "$runner" elasticsearch-kill >"$scratch_dir/dry-run-elasticsearch.log"
rg -q 'scenario=elasticsearch-kill mode=kill service=elasticsearch' "$scratch_dir/dry-run-elasticsearch.log"
DRY_RUN=1 "$runner" auction-kill >"$scratch_dir/dry-run-auction.log"
rg -q 'scenario=auction-kill mode=kill service=auction-service' "$scratch_dir/dry-run-auction.log"
DRY_RUN=1 "$runner" enrichment-pause >"$scratch_dir/dry-run-enrichment.log"
rg -q 'scenario=enrichment-pause mode=pause service=enrichment-consumer' "$scratch_dir/dry-run-enrichment.log"
DRY_RUN=1 "$runner" index-es-kill >"$scratch_dir/dry-run-index-es.log"
rg -q 'scenario=index-es-kill mode=kill service=index-es' "$scratch_dir/dry-run-index-es.log"
DRY_RUN=1 "$runner" index-pgvector-pause >"$scratch_dir/dry-run-index-pgvector.log"
rg -q 'scenario=index-pgvector-pause mode=pause service=index-pgvector' "$scratch_dir/dry-run-index-pgvector.log"
DRY_RUN=1 "$runner" close-kill >"$scratch_dir/dry-run-close.log"
rg -q 'scenario=close-kill mode=kill service=close-worker' "$scratch_dir/dry-run-close.log"
"$runner" --list >"$scratch_dir/scenarios.log"
rg -q '^  nats-kill ' "$scratch_dir/scenarios.log"
rg -q '^  elasticsearch-kill' "$scratch_dir/scenarios.log"
rg -q '^  auction-kill ' "$scratch_dir/scenarios.log"
rg -q '^  enrichment-kill ' "$scratch_dir/scenarios.log"
rg -q '^  index-es-kill ' "$scratch_dir/scenarios.log"
rg -q '^  index-pgvector-kill' "$scratch_dir/scenarios.log"
rg -q '^  close-kill ' "$scratch_dir/scenarios.log"
if rg -q '^kill ' "$scratch_dir/docker.log"; then
  echo "dry run mutated a container" >&2
  exit 1
fi

if CONFIRM_FAULT_INJECTION=1 "$runner" relay-kill >"$scratch_dir/missing-assertion.log" 2>&1; then
  echo "mutating run accepted a missing business assertion" >&2
  exit 1
fi
rg -q 'FAULT_ASSERTION_SCRIPT is required' "$scratch_dir/missing-assertion.log"

: >"$scratch_dir/docker.log"
: >"$scratch_dir/assert.log"
CONFIRM_FAULT_INJECTION=1 \
FAULT_ASSERTION_SCRIPT="$scratch_dir/assert.sh" \
FAULT_DURATION_SECONDS=1 \
RUN_ID=passing \
RESULTS_DIR="$scratch_dir/results" \
"$runner" relay-kill >"$scratch_dir/passing.log"

test "$(wc -l <"$scratch_dir/assert.log")" -eq 3
sed -n '1p' "$scratch_dir/assert.log" | rg -q '^before relay-kill outbox-relay before relay-kill outbox-relay$'
sed -n '2p' "$scratch_dir/assert.log" | rg -q '^during relay-kill outbox-relay during relay-kill outbox-relay$'
sed -n '3p' "$scratch_dir/assert.log" | rg -q '^after relay-kill outbox-relay after relay-kill outbox-relay$'
rg -q '^status=PASS$' "$scratch_dir/results/passing-relay-kill/result.txt"
rg -q '^assertion_phase=after$' "$scratch_dir/results/passing-relay-kill/result.txt"
test -s "$scratch_dir/results/passing-relay-kill/assertion-contract.txt"
test -f "$scratch_dir/results/passing-relay-kill/assertion-before.log"
test -f "$scratch_dir/results/passing-relay-kill/assertion-during.log"
test -f "$scratch_dir/results/passing-relay-kill/assertion-after.log"

: >"$scratch_dir/assert.log"
if CONFIRM_FAULT_INJECTION=1 \
  FAULT_ASSERTION_SCRIPT="$scratch_dir/assert.sh" \
  FAULT_TEST_FAIL_PHASE=during \
  RUN_ID=failing \
  RESULTS_DIR="$scratch_dir/results" \
  "$runner" projector-kill >"$scratch_dir/failing.log" 2>&1; then
  echo "fault run ignored a failed business assertion" >&2
  exit 1
fi
rg -q '^status=FAIL$' "$scratch_dir/results/failing-projector-kill/result.txt"
rg -q '^failed_phase=during$' "$scratch_dir/results/failing-projector-kill/result.txt"
rg -q '^assertion_exit_code=23$' "$scratch_dir/results/failing-projector-kill/result.txt"
rg -q '^compose -f .* start projector$' "$scratch_dir/docker.log"
if rg -q '^compose -f .* up -d projector$' "$scratch_dir/docker.log"; then
  echo "kill recovery recreated dependencies instead of preserving service identity" >&2
  exit 1
fi

: >"$scratch_dir/docker.log"
FAULT_TEST_START_FAIL_SENTINEL="$scratch_dir/start.fail.once" \
CONFIRM_FAULT_INJECTION=1 \
FAULT_ASSERTION_SCRIPT="$scratch_dir/assert.sh" \
FAULT_TEST_FAIL_START_SERVICE=projector \
RUN_ID=fallback \
RESULTS_DIR="$scratch_dir/results" \
"$runner" projector-kill >"$scratch_dir/fallback.log"
rg -q '^compose -f .* start projector$' "$scratch_dir/docker.log"
rg -q '^compose -f .* up -d --no-deps projector$' "$scratch_dir/docker.log"

: >"$scratch_dir/assert.log"
CONFIRM_FAULT_INJECTION=1 \
FAULT_ASSERTION_SCRIPT="$scratch_dir/assert.sh" \
FAULT_ASSERT_AFTER_SERVICE_START=1 \
FAULT_DURATION_SECONDS=1 \
RUN_ID=early-after \
RESULTS_DIR="$scratch_dir/results" \
"$runner" projector-pause >"$scratch_dir/early-after.log"
test "$(wc -l <"$scratch_dir/assert.log")" -eq 3
rg -q '^assert_after_service_start=1$' "$scratch_dir/results/early-after-projector-pause/assertion-contract.txt"
rg -q '^status=PASS$' "$scratch_dir/results/early-after-projector-pause/result.txt"

if CONFIRM_FAULT_INJECTION=1 \
  FAULT_ASSERTION_SCRIPT="$scratch_dir/assert.sh" \
  FAULT_ASSERT_AFTER_SERVICE_START=maybe \
  RUN_ID=invalid-early \
  RESULTS_DIR="$scratch_dir/results" \
  "$runner" projector-pause >"$scratch_dir/invalid-early.log" 2>&1; then
  echo "fault run accepted an invalid early-after flag" >&2
  exit 1
fi
rg -q 'FAULT_ASSERT_AFTER_SERVICE_START must be 0 or 1' "$scratch_dir/invalid-early.log"

if CONFIRM_FAULT_INJECTION=1 \
  FAULT_ASSERTION_SCRIPT="$scratch_dir/assert.sh" \
  FAULT_ASSERT_AFTER_SERVICE_START=1 \
  FAULT_DURATION_SECONDS=1 \
  FAULT_RECOVERY_TIMEOUT_SECONDS=1 \
  FAULT_TEST_SLEEP_AFTER_SECONDS=2 \
  FAULT_TEST_HEALTH_STATE=starting \
  RUN_ID=unhealthy-after \
  RESULTS_DIR="$scratch_dir/results" \
  "$runner" projector-pause >"$scratch_dir/unhealthy-after.log" 2>&1; then
  echo "early after assertion bypassed the final Docker health proof" >&2
  exit 1
fi
rg -q '^failed_phase=recovery$' "$scratch_dir/results/unhealthy-after-projector-pause/result.txt"
rg -q '^recovered_state=starting$' "$scratch_dir/results/unhealthy-after-projector-pause/result.txt"

echo "Fault injection requires bounded before/during/after business assertions and preserves recovery on failure."
