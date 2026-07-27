# End-to-End Projection Gate Design

Date: 2026-07-27

## 1. Purpose

Redis outbox pending length protects the first event hop, but it cannot detect
the failure mode where Relay has drained Redis into Kafka while Kafka-to-MySQL
projection is stopped. In that state Redis pending can remain low while the
system continues accepting work whose durable bids, orders and read models are
not becoming visible.

The end-to-end projection gate closes that gap. It lets `auction-service`
admit a new runtime command only while every Runtime Topic partition has a
healthy path from Kafka to the authoritative MySQL Projector offset.

## 2. Fixed command boundary

The gate controls commands that create or enlarge runtime work:

- start a lot;
- place a bid;
- synchronize a live lot configuration.

Cancellation and expiry close remain available while the gate is closed.
They reduce or terminate live work and are required to converge safely. The
standalone close worker therefore does not depend on the projection gate.

The existing Redis Lua outbox pending limit remains mandatory. The new gate is
an additional end-to-end layer, not a replacement for atomic Redis-local
backpressure.

## 3. Authority and architecture

`auction-service` owns one in-process `projectiongate.Guard`. The guard reads:

1. Kafka earliest and exclusive latest offsets for every partition of
   `auction.runtime.projection.v1`;
2. MySQL `auction_projection_partition_offsets`, whose `next_offset` is the
   first Runtime Fact not committed by Projector;
3. the broker timestamp of the record at each lagging partition's DB
   `next_offset`.

The guard does not read Prometheus to make a business decision and Projector
does not publish a second control state into Redis. Kafka and MySQL are the
direct authorities for the backlog being gated.

`auction-service` receives only Runtime Topic `Read` and `Describe` Kafka ACLs.
The gate uses direct partition assignment, has no consumer group, never commits
Kafka offsets and has no Kafka write permission. Runtime Fact payloads fetched
for age calculation are discarded without logging or exporting them.

The guard refreshes a local immutable snapshot in the background. Command
checks use only that bounded-age snapshot and never perform a Kafka or MySQL
round trip on the request path.

## 4. Snapshot and evaluation

One snapshot contains:

- check time and readiness;
- stable close reason;
- Kafka partition count;
- total and maximum per-partition lag;
- maximum oldest-unprojected age;
- minimum retention headroom;
- per-partition earliest, DB next, latest, lag and oldest timestamp;
- consecutive healthy sample count.

Every Kafka partition must have exactly one valid MySQL offset. Extra MySQL
partition rows for the Runtime Topic are topology drift and also close the
gate.

For each partition:

```text
earliest <= DB next_offset <= latest
lag = latest - DB next_offset
```

`DB next_offset < earliest` is a retention cliff. `DB next_offset > latest` is
an offset fork or Kafka rollback. Both close the gate immediately.

When `lag > 0`, the guard directly reads the Kafka record at `DB next_offset`.
The returned offset must match exactly and its broker timestamp must be
positive and no more than five minutes in the future. Its age is the
oldest-unprojected age for that partition. When `lag == 0`, that age is zero
and no record fetch is performed.

Retention headroom is:

```text
configured Runtime Topic retention - oldest-unprojected age
```

The configured retention is an operational contract and must match the topic
initializer and broker policy. Kafka remains the source of earliest/latest
offset truth; the configured duration is used only for the time-headroom gate.

## 5. Default safety policy

Production defaults are:

| Setting | Default |
|---|---:|
| refresh interval | 2 seconds |
| one refresh timeout | 1.5 seconds |
| maximum snapshot staleness | 6 seconds |
| maximum lag per partition | 1000 records |
| maximum oldest-unprojected age | 30 seconds |
| Runtime Topic retention | 2160 hours / 90 days |
| minimum retention headroom | 168 hours / 7 days |
| healthy samples required to reopen | 3 |

The gate closes on the first unsafe sample or dependency error. It reopens only
after three consecutive complete healthy samples. A threshold failure resets
the healthy count. Snapshot staleness is evaluated again on every admission
status and command check, so a stopped monitor cannot leave the gate open.

All settings are explicit environment values:

- `AUCTION_PROJECTION_GATE_ENABLED`;
- `AUCTION_PROJECTION_GATE_REFRESH_INTERVAL`;
- `AUCTION_PROJECTION_GATE_REFRESH_TIMEOUT`;
- `AUCTION_PROJECTION_GATE_MAX_STALENESS`;
- `AUCTION_PROJECTION_GATE_MAX_LAG_RECORDS`;
- `AUCTION_PROJECTION_GATE_MAX_OLDEST_AGE`;
- `AUCTION_PROJECTION_GATE_RUNTIME_TOPIC_RETENTION`;
- `AUCTION_PROJECTION_GATE_MIN_RETENTION_HEADROOM`;
- `AUCTION_PROJECTION_GATE_HEALTHY_POLLS_TO_OPEN`.

Production startup rejects a disabled gate, non-positive thresholds, refresh
timeout greater than or equal to the refresh interval, max staleness shorter
than two refresh intervals, and minimum headroom greater than or equal to
retention. Development may disable the gate explicitly for isolated unit or UI
work; controlled Compose and Kubernetes deployments enable it.

## 6. Runtime behavior and error contract

`Store` accepts a small `RuntimeAdmissionGate` interface. The gate is checked
immediately before `ExecuteStartLot`, `ExecutePlaceBid` and
`ExecuteSyncLotConfig` perform locking, identity generation or Redis work.
There is no gate check in cancellation or close execution.

A rejected command returns the stable business code `OVERLOADED` with a retry
instruction. It must not be translated to a bid-rule rejection, projection
pending identity conflict or internal error. No Runtime Fact, Redis mutation,
MySQL mutation or outbox entry may be created by a rejected command.

The guard is enforced at application admission instead of Kubernetes service
readiness. `/readyz` continues to report process, Store and transport dependency
availability so a closed gate does not remove every `auction-service` Pod from
service endpoints. This keeps cancellation and expiry close reachable and lets
start, bid and config-sync requests receive the stable `OVERLOADED` result.

`/admissionz` independently reports the gate as `200 open` or `503 closed` for
operations and alert verification. It is not a Kubernetes readiness probe.
Liveness remains independent so the process keeps refreshing and can recover
without restart.

## 7. Stable close reasons

Externally exported reasons are a bounded enum:

- `uninitialized`;
- `recovering`;
- `kafka_unavailable`;
- `mysql_unavailable`;
- `partition_mismatch`;
- `offset_missing`;
- `retention_cliff`;
- `offset_ahead`;
- `record_missing`;
- `record_timestamp_invalid`;
- `lag_limit`;
- `oldest_age_limit`;
- `retention_headroom`;
- `snapshot_stale`.

Dependency error text is logged in bounded form but is not used as a metric
label or returned to clients.

## 8. Observability

The application exports:

- `auction_projection_gate_ready`;
- `auction_projection_gate_rejection_total{reason}`;
- `auction_projection_gate_lag_records`;
- `auction_projection_gate_max_partition_lag_records`;
- `auction_projection_gate_oldest_age_ms`;
- `auction_projection_gate_retention_headroom_ms`;
- `auction_projection_gate_snapshot_age_ms`;
- `auction_projection_gate_state{reason}` as a one-hot bounded state.

`/metrics/runtime-projection` returns the same safe snapshot without Runtime
Fact payloads, user identities, DSNs or Kafka credentials. Alerts fire when the
gate is closed, oldest age approaches its limit or headroom approaches its
minimum. Prometheus remains observational; it cannot reopen the gate.

## 9. Deployment and security

Controlled local Compose adds the Kafka broker address and enables the gate for
`auction-service`. Production Compose reuses the existing audited Kafka client
environment. Kubernetes mounts a dedicated
`auction-kafka-auction-service` client properties Secret read-only and adds an
ExternalSecret example for that principal.

Kafka ACL automation grants `auction-service` only Runtime Topic `Read` and
`Describe`. It grants no group, write, cluster-idempotent-write or wildcard
permission.

The Kubernetes configuration contains all gate thresholds and the static
Runtime Topic retention contract. Zero-residue and wiring checks require the
gate to be enabled in controlled production manifests and reject a legacy or
environment-interpolated bypass.

## 10. Verification

Unit tests cover:

- complete healthy snapshots and three-sample reopen hysteresis;
- missing and extra partitions;
- missing DB offsets, retention cliffs and offsets ahead of Kafka;
- per-partition lag, oldest age and retention-headroom limits;
- Kafka/MySQL errors, malformed record offsets/timestamps and stale snapshots;
- direct partition reads without consumer-group commits;
- start, bid and live-config rejection before mutation;
- cancellation remaining available while the gate is closed;
- stable `OVERLOADED` result mapping, admission-status failure and service
  readiness independence;
- production configuration rejecting disablement or invalid thresholds.

Race tests exercise concurrent refresh, admission-status and command checks. Existing
Projector MySQL integration tests remain the proof that DB offsets advance only
with the projection transaction. A target-environment drill stops all Projector
replicas while Relay keeps draining Redis and proves:

1. Kafka lag and oldest age increase;
2. the gate closes within the configured window even when Redis pending is low;
3. new starts, bids and config syncs return `OVERLOADED` with zero new Runtime
   Facts;
4. cancellation and close still settle live lots;
5. Projector recovery advances DB offsets;
6. `/readyz` remains healthy while Store dependencies are healthy, and three
   healthy samples reopen `/admissionz` and command admission.

## 11. Non-goals

- Replacing Redis Lua pending limits.
- Pausing cancellation or close commands.
- Using Prometheus, an operator toggle or a Redis heartbeat as authority.
- Committing Kafka consumer-group offsets from `auction-service`.
- Automatically changing Runtime Topic retention.
- Claiming target-environment capacity or HA acceptance from unit tests.
