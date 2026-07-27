# End-to-end projection gate runbook

This runbook covers the `auction-service` admission gate between accepted
Redis runtime facts and their MySQL projection. The gate is a safety control,
not a traffic-shaping feature.

## Protected commands

When the gate is closed, these commands return stable business code
`OVERLOADED` before event ID generation, MySQL mutation or Redis access:

- start lot;
- place bid;
- synchronize a live lot configuration.

Operator cancellation and expiry close remain available. They reduce or
terminate runtime exposure and must not be disabled by projector lag.

The gate closes immediately after one unhealthy sample. It reopens only after
three consecutive healthy samples. Operators cannot manually force it open.

## Observe

Use the auction-service operations endpoint:

```bash
curl -fsS http://auction-service:18086/metrics/runtime-projection | jq .
curl -fsS http://auction-service:18086/readyz
curl -fsS http://auction-service:18086/admissionz
```

The JSON snapshot contains partition bounds, the MySQL `next_offset`, lag,
oldest-record age and retention headroom. It contains no Runtime Fact payload,
identity, DSN or Kafka credential.

`/readyz` reports service dependencies and deliberately remains independent of
the projection gate so cancellation and expiry close stay routable.
`/admissionz` returns `200 {"status":"open"}` only when risk-increasing
commands are admitted; a closed gate returns `503 {"status":"closed"}`.

Primary Prometheus signals:

```text
auction_projection_gate_ready
auction_projection_gate_state{reason}
auction_projection_gate_rejection_total{reason}
auction_projection_gate_lag_records
auction_projection_gate_max_partition_lag_records
auction_projection_gate_oldest_age_ms
auction_projection_gate_retention_headroom_ms
auction_projection_gate_snapshot_age_ms
```

## Reason-to-action matrix

| Reason | Meaning | Required action |
|---|---|---|
| `uninitialized` | No complete Kafka/MySQL sample yet | Check the dedicated Kafka Secret, MySQL connectivity and startup logs |
| `recovering` | Samples are healthy but the three-poll reopening threshold is not complete | Wait; do not restart healthy Pods |
| `kafka_unavailable` | Metadata, ListOffsets or direct record read failed | Restore broker reachability and the auction-service `Read/Describe` ACL |
| `mysql_unavailable` | DB offset query failed | Restore the Store-owned MySQL pool; do not create a second pool |
| `partition_mismatch` | Kafka partition metadata or the DB partition set is structurally inconsistent | Stop partition changes and compare Runtime Topic metadata with projector ownership |
| `offset_missing` | Kafka has a partition without a valid MySQL offset row | Restore Projector assignment; never initialize to latest by hand |
| `retention_cliff` | DB `next_offset` is below Kafka earliest | Treat as P0 and use the audited synthetic repair workflow |
| `offset_ahead` | DB `next_offset` exceeds Kafka latest | Stop Projector and investigate offset corruption before any replay |
| `record_missing` | The exact DB next offset did not yield that Kafka record | Stop Projector and use gap recovery; do not skip to the next record |
| `record_timestamp_invalid` | The oldest record has no credible broker timestamp | Check producer/broker time and Runtime Topic integrity |
| `lag_limit` | One partition exceeds the configured record threshold | Restore or scale Projector and identify the blocked partition |
| `oldest_age_limit` | The oldest unprojected record exceeds the age threshold | Restore Projector; age is the user-visible durability delay |
| `retention_headroom` | Estimated time before the retention cliff is below the minimum | Increase retention if still safe, restore projection, and prepare repair evidence |
| `snapshot_stale` | The last complete sample is older than the fail-closed limit | Restore the sampling loop or its dependencies |

## Projector pause drill

Run only in a disposable or explicitly authorized environment. The repository
includes an executable three-phase assertion that creates an isolated live lot,
drives one accepted fact while Projector is unavailable, waits for the gate to
close, proves an `OVERLOADED` bid adds no Kafka fact, cancels the lot while the
gate is closed, and verifies the recovered MySQL projection:

```bash
CONFIRM_FAULT_INJECTION=1 \
FAULT_ASSERTION_SCRIPT="$PWD/scripts/assert-projector-recovery.mjs" \
FAULT_ASSERT_AFTER_SERVICE_START=1 \
FAULT_DURATION_SECONDS=40 \
./scripts/run-fault-injection.sh projector-pause
```

The assertion uses `BASE_URL` (default `http://127.0.0.1:18080`) and
`AUCTION_SERVICE_OPERATIONS_URL` (default `http://127.0.0.1:18086`). It queries
MySQL through the exact Compose file and `mysql` service selected by the fault
runner, so it does not require host MySQL credentials. The target environment
must be isolated from unrelated auction traffic because the rejected-command
check compares the complete Runtime Topic partition watermarks before and after
that command. Override `FAULT_MYSQL_SERVICE` only when the Compose service has a
different name.

The cross-phase state file is mode `0600` and contains only generated usernames,
business IDs and watermarks. Access tokens and passwords are never persisted in
the archived result directory; each active phase logs in again and keeps the
token only in memory.

`FAULT_ASSERT_AFTER_SERVICE_START=1` invokes the `after` assertion as soon as
the replacement/unpaused process is running, before waiting for Docker's
coarser health interval. This makes the two closed `recovering` polls observable;
the fault runner still requires Docker health after the business assertion.
Observing `recovering` is a hard gate for `projector-pause` by default. Set
`PROJECTOR_ASSERTION_REQUIRE_RECOVERING=1` for `projector-kill` when the target
platform exposes the replacement process early enough. Final admission always
requires `healthy`, zero lag and the configured healthy-poll threshold.

The assertion must prove all of the following:

1. before: auction-service readiness and admission status are healthy and the
   DB offsets equal the expected Kafka positions;
2. during: deterministic accepted work makes Kafka-to-MySQL lag cross the
   configured threshold;
3. during: new start, bid and config-sync requests return `OVERLOADED` and do
   not add Redis outbox entries, Kafka Runtime records or MySQL rows;
4. during: operator cancellation and expiry close still commit through Lua;
   `/readyz` remains healthy while `/admissionz` is closed;
5. after: Projector resumes from DB offsets, inbox and lot versions stay
   continuous, lag returns within threshold, and the gate remains
   `recovering` for two healthy samples before `/admissionz` and command
   admission reopen on the third;
6. after: no new P0/P1 projector finding, duplicate order or duplicate bid is
   present.

Archive the assertion logs, Redis outbox lengths, Kafka earliest/latest,
MySQL offsets, `/readyz`, `/admissionz`, `/metrics/runtime-projection` snapshots
and Prometheus alert history in the fault runner's result directory. A healthy
process alone is not acceptance evidence.

## Security boundary

Kubernetes mounts `auction-kafka-auction-service` read-only. Its principal has
only `Read` and `Describe` on `auction.runtime.projection.v1`; it owns no group
and has no topic or cluster write permission. The gate never commits Kafka
offsets and never writes to Kafka, Redis or MySQL.
