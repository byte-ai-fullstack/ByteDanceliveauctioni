# Target infrastructure runbook

This runbook is the executable operations contract for L2 of
[`REFACTORING_PLAN.md`](../../../docs/REFACTORING_PLAN.md). It covers the infrastructure required by the target event path:

```text
Redis Lua + Redis outbox -> Kafka -> Projector -> MySQL domain outbox -> Kafka
                                      |                              |
                                      +-> NATS realtime              +-> ES / pgvector
```

It does not make the single-host Compose files a production topology. Production acceptance requires the same checks against three independent failure domains and evidence retained with the release.

## 1. Environment boundary

| Environment | Purpose | Accepted topology | Evidence value |
|---|---|---|---|
| Local | Development and fast contract checks | Single-node Kafka, Redis, MySQL, NATS, ES and pgvector | Functional only |
| CI | Clean-room repeatability | Disposable single-node stack plus disposable single-host HA lab | Protocol, configuration and quorum behavior |
| Three-host test | Failure-domain validation | One Kafka/Redis/Sentinel/NATS member per host/AZ | Required before production rollout |
| Production | Customer traffic | Managed Multi-AZ where possible; otherwise independently operated quorum members | Release evidence |

The local Compose credentials ending in `local_only` or `test-only` are disposable. They must never be copied into a shared environment.

## 2. Immutable inputs and static gates

All third-party images in `deploy/**/*.yml` are pinned by tag and multi-platform digest and recorded in [`deploy/images.lock`](../../deploy/images.lock). Kubernetes application images are supplied by the release renderer as a versioned registry reference plus digest. The single-host production Compose path has no registry, so the deployment pipeline records the exact backend and customized Elasticsearch content IDs in `/opt/live-auction/.release.env`; tag-only application images are rejected.

`scripts/deploy-prod.sh` builds both project images, packages them by image ID, loads them remotely, verifies `docker image inspect` returns the recorded IDs, and only then runs Compose with `--no-build`. Frontend-only deployment also revalidates the existing release manifest before touching the running stack. Operators must not hand-edit `.release.env`.

Run before bringing up any environment:

```bash
bash scripts/check-image-lock.sh
docker compose -f deploy/docker-compose.yml config --quiet
docker compose -f deploy/ha/docker-compose.yml config --quiet
bash test/infra/kafka-init-topics.test.sh
bash test/infra/kafka-acls.test.sh
bash test/infra/mysql-connection-budget.test.sh
bash test/infra/mysql-backup-restore.test.sh
bash test/infra/prod-release-images.test.sh
bash scripts/check-l2-monitoring.sh
```

Never promote a version change when one of these checks fails. Update a tag and its digest together and repeat both smoke and fault drills.

## 3. Local and CI smoke test

The smoke runner starts only target infrastructure and exporters; it does not depend on the legacy application runtime:

```bash
bash scripts/run-l2-infra-smoke.sh | tee test-results/l2-infra-smoke.log
```

Set `KEEP_L2_INFRA=1` only when manual inspection is needed. The runner verifies:

- seven explicit Kafka topics, with `auction.runtime.projection.v1` retained for 90 days;
- Redis AOF/everysec, `noeviction` and persistence write safety;
- NATS and Elasticsearch health;
- the pgvector extension;
- MySQL `innodb_flush_log_at_trx_commit=1`, `sync_binlog=1` and ROW binlogs;
- live metrics from Kafka, Redis, NATS, MySQL, Elasticsearch and PostgreSQL exporters.

CI archives the complete log. A local log is diagnostic evidence only because all stateful services still share one host.

## 4. Three failure domains

Minimum test placement:

| Failure domain | Stateful members |
|---|---|
| Host/AZ A | Kafka broker/controller 1, Redis primary candidate, Sentinel 1, NATS 1 |
| Host/AZ B | Kafka broker/controller 2, Redis replica candidate, Sentinel 2, NATS 2 |
| Host/AZ C | Kafka broker/controller 3, Redis replica candidate, Sentinel 3, NATS 3 |

Do not place two members of the same quorum on one VM, hypervisor failure domain or zone. MySQL, Elasticsearch and pgvector should use their own managed Multi-AZ service or separate nodes; they are not to be squeezed onto these three small test machines for production.

The repository HA Compose is a single-host correctness lab:

```bash
bash scripts/run-l2-ha-fault-drill.sh | tee test-results/l2-ha-fault-drill.log
```

It proves the protocol behavior below but cannot prove rack, switch, disk-controller or AZ independence. Repeat the cases manually or through the infrastructure platform on the three-host environment and record member IDs, timestamps and monitoring screenshots.

## 5. Kafka

### 5.1 Required configuration

- KRaft quorum, at least three brokers across three failure domains;
- topic replication factor 3 and `min.insync.replicas=2`;
- producer `acks=all` with idempotence enabled;
- automatic topic creation and unclean leader election disabled;
- `auction.runtime.projection.v1` keyed by `lot_id`, 24 partitions and 90-day retention;
- rack/AZ awareness in shared environments.

Initialize or reconcile topics:

```bash
KAFKA_BOOTSTRAP_SERVERS=kafka-1:9092,kafka-2:9092,kafka-3:9092 \
KAFKA_REPLICATION_FACTOR=3 \
KAFKA_MIN_INSYNC_REPLICAS=2 \
bash scripts/kafka-init-topics.sh
```

The initializer treats every existing topic partition count as immutable and refuses both undersized and oversized counts. It also rejects a replication-factor mismatch instead of pretending that a topic-config update changed replica placement. It never runs `--alter --partitions`: increasing a keyed topic changes key-to-partition routing, while Kafka cannot shrink it afterward. Follow `docs/KAFKA_PARTITION_MIGRATION_RUNBOOK.md` for a drained window or versioned-topic migration; use an approved Kafka replica-reassignment plan for replication-factor drift, and never change active Runtime Topic partitioning in place.

### 5.2 ACLs and transport

Shared environments use SASL_SSL with SCRAM-SHA-512 or cloud IAM. Create one principal per executable and apply the least-privilege ACL set:

```bash
KAFKA_BOOTSTRAP_SERVERS=kafka.example:9093 \
KAFKA_COMMAND_CONFIG=/run/secrets/kafka-admin.properties \
bash scripts/kafka-apply-acls.sh
```

[`deploy/kafka/client.properties.example`](../../deploy/kafka/client.properties.example) is a key contract, not a credential file. Materialize the real file from External Secrets or a CSI Secret Store with mode `0400`.
The workload-to-key mapping is declared in [`deploy/kubernetes/secrets`](../../deploy/kubernetes/secrets/README.md); the example contains references only and is not applied until the environment provides its `ClusterSecretStore`.

### 5.3 Failure acceptance

| Injection | Expected result |
|---|---|
| Stop one broker | Leader moves; `acks=all` remains available with two ISR members |
| Stop a second broker | New writes are rejected or time out; no under-replicated write is acknowledged |
| Restore brokers | ISR returns to full membership before the projection gate reopens |
| Pause Projector | Lag and oldest-event age rise; runtime facts remain replayable |
| Fill one test broker past 75%/85% | Warning/critical disk alerts fire; traffic is moved before exhaustion |

For disk tests, use a dedicated disposable Kafka volume. Confirm the mount and free space first, create only a bounded filler file inside that volume, then remove that exact file after alert evidence is captured. Never run a fill test against an unknown or production filesystem.

The auction-service end-to-end admission behavior, bounded reasons and required
Projector pause evidence are defined in
[`PROJECTION_GATE_RUNBOOK.md`](../PROJECTION_GATE_RUNBOOK.md).

## 6. Redis and Sentinel

### 6.1 Required configuration

```conf
appendonly yes
appendfsync everysec
min-replicas-to-write 1
min-replicas-max-lag 10
maxmemory-policy noeviction
stop-writes-on-bgsave-error yes
repl-diskless-sync yes
```

Application writes use a Sentinel-discovered primary. In L3, `EVALSHA` and `WAIT` must run on the same borrowed connection. A `WAIT` timeout means “the state transition may have succeeded but replica durability was not confirmed”; it never means the bid can safely be retried as a new command.

### 6.2 Failure acceptance

| Injection | Expected result |
|---|---|
| Stop current primary after a replicated write | Sentinel promotes a replica and the acknowledged key remains |
| Observe a different primary `run_id` | Auction write readiness closes until generation reconciliation completes |
| Lose Sentinel notification | Periodic identity polling still detects the generation change |
| Disconnect both replicas | `min-replicas-to-write` prevents unsafe writes |
| Corrupt an AOF copy | Recovery is exercised on a copy, followed by active-runtime reconciliation from MySQL/Kafka |

The L2 drill proves Sentinel promotion and retained acknowledged data. L3 tests must additionally prove application fencing, ambiguous-response idempotency and Redis-outbox lease takeover.

## 7. NATS Core

NATS is the cross-gateway realtime fanout bus only. JetStream remains disabled; Kafka carries durable/replayable events.

Requirements:

- three Core NATS nodes, one per AZ;
- TLS in shared environments;
- separate APP and SYS accounts;
- `auction-service` may publish only approved realtime prefixes;
- `domain-relay` uses a separate publish-only principal for post-Kafka-ACK order `READY` acceleration and may not subscribe to room subjects;
- `gateway` may subscribe only to approved public/personal/admin prefixes;
- room and user subject tokens are canonical encoded values, never raw IDs containing `.`, `*` or `>`.
- `auction_nats_subscriptions` must match the number of non-empty rooms on each Gateway; `AuctionNATSSubscriptionDrift` firing for five minutes indicates retain/release leakage or missing delivery coverage.

Failure acceptance:

| Injection | Expected result |
|---|---|
| Stop one NATS node | Remaining nodes keep a route and accept connections |
| Partition gateway from NATS | Bidding still returns synchronously; realtime is marked degraded |
| Partition domain-relay from NATS | Domain Kafka publication remains successful; winners recover `READY` by polling `/api/rooms/{id}/me` |
| Restore route | Gateway reconnects and refreshes its Redis snapshot before applying newer increments |

The first case belongs to L2. Client reconnect, version merge and private-message correctness are L4 acceptance tests.

## 8. MySQL

### 8.1 Durability and migrations

Required server settings:

```text
innodb_flush_log_at_trx_commit=1
sync_binlog=1
binlog_format=ROW
```

Schema changes run through `/app/auction-migrate`; application Pods must not run `AutoMigrate`. Production prefers managed Multi-AZ. A self-managed deployment requires InnoDB Cluster, Group Replication and MySQL Router with a tested failover policy.

### 8.2 Connection budget

Validate the initial 600-connection budget:

```bash
bash scripts/check-mysql-connection-budget.sh
```

Every workload must set MaxOpen, MaxIdle, ConnMaxLifetime and ConnMaxIdleTime explicitly. Keep at least 20% for failover, migrations, exporters and operators. The MySQL exporter principal is limited to three connections.

### 8.3 Backup, restore and PITR

Create a full logical backup using a root-protected password file:

```bash
MYSQL_HOST=mysql.example \
MYSQL_USER=backup \
MYSQL_PASSWORD_FILE=/run/secrets/mysql-backup-password \
BACKUP_DIR=/srv/auction-backups \
bash scripts/mysql-backup.sh
```

Restore only into an isolated `restore_*` database and keep it for application-level verification:

```bash
MYSQL_HOST=mysql-restore.example \
MYSQL_USER=restore_operator \
MYSQL_PASSWORD_FILE=/run/secrets/mysql-restore-password \
BACKUP_FILE=/srv/auction-backups/live_auction-YYYYmmddTHHMMSSZ.sql.gz \
RESTORE_DATABASE=restore_quarterly_YYYYqN \
RESTORE_CONFIRM=restore_quarterly_YYYYqN \
bash scripts/mysql-restore-verify.sh
```

Production also archives encrypted binlogs to independent object storage. A PITR drill restores the latest full backup, applies binlogs only through the chosen event/time with `mysqlbinlog --stop-datetime` or `--stop-position`, then verifies:

- migration history and checksums;
- runtime projection offsets and inbox rows are transactionally aligned;
- order totals and settlement counts reconcile;
- the restored system does not publish domain events until explicitly unfenced.

Schedule automated backups daily, restore verification at least monthly and full PITR/failover exercises quarterly. Record RPO, RTO, backup checksum, restored database name and reconciliation results.

## 9. Elasticsearch and pgvector

Both stores are rebuildable projections and must never become auction truth sources.

- Elasticsearch: managed Multi-AZ or three master-eligible plus at least three data nodes, zone awareness, replica count at least one, daily SLM snapshots and periodic restore checks.
- pgvector: managed Multi-AZ or primary/replica PostgreSQL; benchmark HNSW/IVFFlat parameters on the real corpus.
- embedding credentials and spending limits are separate secrets owned by `index-pgvector`.
- index outages disable their feature only; bidding, settlement and authoritative lot reads remain available.

搜索性能门禁分别以 `auction_search_retrieval_duration_ms` 的 `source="elasticsearch"` 和 `source="pgvector"` 统计检索阶段延迟，目标均为 p99 `< 1000ms`。该口径排除外部 LLM 生成时间；完整买家咨询接口时延作为单独体验指标记录。容量环境将 AI provider 固定为 mock 后，按 [`search-retrieval-capacity-gate.md`](../performance/search-retrieval-capacity-gate.md) 运行 5000 次检索并归档 JSON 与 Prometheus 时间窗。

L5 supplies versioned alias swaps, replay/rebuild commands and cross-store reconciliation. Until then, the L2 checks only prove storage and exporter availability.

## 10. Monitoring and alert evidence

Prometheus loads [`target-infrastructure.yml`](../../deploy/prometheus/rules/target-infrastructure.yml); Grafana provisions `Live Auction · Target Infrastructure` from [`target-infrastructure.json`](../../deploy/grafana/dashboards/target-infrastructure.json).

Every shared environment must retain evidence for:

- exporter availability;
- Kafka broker count, under-replicated partitions and consumer lag;
- Redis evictions, persistence failures, memory and replica state;
- NATS routes, connections and slow consumers;
- MySQL connection utilization and replication/failover state;
- Elasticsearch cluster health and pgvector availability;
- embedding token throughput by reported/estimated source and cumulative configured-cost estimate;
- host filesystem and Kubernetes PVC watermarks for every durable store;
- host memory, bounded Redis memory and Elasticsearch JVM heap watermarks.

Production Redis must set a tested `maxmemory` below its container or VM limit and keep `maxmemory-policy=noeviction`. Reserve headroom for allocator fragmentation, AOF rewrite/fork behavior, replication buffers and the operating system; host OOM is not a capacity policy. `AuctionRedisMemoryLimitMissing` is therefore critical, not informational.

Host rules cover every non-ephemeral filesystem exposed by node_exporter. Kubernetes platforms must additionally scrape kubelet `kubelet_volume_stats_used_bytes` and `kubelet_volume_stats_capacity_bytes`; managed Kafka/MySQL/Redis/Elasticsearch/PostgreSQL services must map their provider disk and memory metrics to equivalent 75% warning and 85% critical alerts. A single root-filesystem graph is not evidence that every durable volume is covered.

Set `AUCTION_EMBEDDING_COST_PER_MILLION_TOKENS` to the provider's current input-token price in the team's billing currency. `auction_embedding_tokens_estimate_total` prefers provider-reported usage and labels character-count fallback as `source="estimated"`; `auction_embedding_cost_estimate_total` is a per-process estimate that resets on restart. Configure environment-specific budget alerts outside the repository and reconcile them with the provider invoice.

Alert tests prove expressions can enter firing state. The fault drills prove the real exporter labels and runtime behavior. Neither one alone is sufficient.

Disk and memory drills use disposable capacity environments only. Fill one explicitly identified test volume with a bounded file to cross 75% and 85%, and apply a bounded workload or platform limit to cross the memory thresholds. Record the exact mount/PVC/instance labels, alert transition times and recovery. Never fill an unresolved path, production filesystem or shared host.

## 11. Evidence record

Each execution stores an immutable record containing:

```text
git commit
image lock checksum
environment and failure-domain mapping
start/end UTC timestamps
injected failure and exact target member
observed alert names and firing timestamps
Kafka topic configuration and ISR result
Redis old/new primary identity and retained-key result
NATS route counts before/during/after failure
MySQL backup checksum, restore database and RPO/RTO
operator and reviewer
```

CI artifacts are retained for 14 days for rapid feedback. Release evidence and quarterly recovery evidence belong in the long-term engineering evidence store, not only in a CI artifact.
