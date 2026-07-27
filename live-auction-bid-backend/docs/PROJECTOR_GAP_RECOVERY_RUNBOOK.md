# Projector gap recovery runbook

`auction-projection-repair` diagnoses and replays retained records from `auction.runtime.projection.v1`. When those records have crossed Kafka retention, its break-glass `synthetic` mode can apply an independently reviewed evidence bundle. MySQL `auction_projection_partition_offsets` is the only recovery cursor. The command never rewrites a Kafka consumer-group offset and never advances a DB offset without applying one complete Runtime Fact through the normal Projector transaction.

## Safety contract

- `diagnose`, `replay` and `synthetic` without `--execute` are read-only.
- An executing replay requires the exact observed DB `next_offset`, an inclusive last Kafka offset, an operator, a reason and an exact confirmation string.
- At most 1000 records may be replayed per invocation.
- The complete range is decoded and lot-version continuity is checked before the first write.
- Each record still goes through Projector inbox, business projection, domain outbox and DB offset in one transaction.
- The command aborts if another Projector advances the partition concurrently.
- Success requires the final DB offset and every affected lot's `last_event_id`, `last_lot_version`, `auction_lots.version` and `canonical_hash` to match the replayed facts.
- Every executing attempt writes `auction_projection_repair_audit`. A failed or interrupted attempt remains auditable and may be safely diagnosed again.
- The Job uses the dedicated `projection-repair` Kafka principal, which has only `Read` and `Describe` on the Runtime Topic and no consumer-group or Kafka write ACL.
- Synthetic repair requires an exact bundle SHA-256, separate preparer and executor identities, and a change or incident ticket embedded in the reviewed bundle.
- A synthetic bundle is opened without following symbolic links, must be a read-only regular file, and is re-opened and re-hashed immediately before execution.
- Redis may be compared with the evidence but may never generate missing facts or serve as authority for a synthetic repair.

Never update `auction_projection_partition_offsets`, `auction_projection_inbox`, `auction_lot_projection_state` or `auction_lots.version` manually.

## 1. Diagnose while Projector is running

Identify the partition from `/readyz`, `auction_projector_paused` or the Projector logs, then inspect the DB cursor and nearby Kafka/inbox identities:

```bash
/app/auction-projection-repair diagnose \
  --partition=PARTITION \
  --before=3 \
  --after=10
```

Interpret the JSON:

- `database_offset.next_offset` is the first record that MySQL has not committed.
- Kafka `latest` is exclusive.
- `inbox_status=matched` proves the original event identity and payload hash agree with the committed inbox row.
- `inbox_status=missing` is expected at and after `next_offset`.
- `inbox_status=conflict` is a P0 identity fork; do not replay.
- `retention_cliff=true` means the DB cursor is outside retained Kafka history. Stop this procedure and escalate to synthetic repair review.

The tool does not initialize a missing DB offset. A partition that has never been assigned must first be initialized by the normal Projector startup path.

## 2. Preview the exact replay

Use the observed DB cursor as `--expected-next-offset`. Keep the first run small enough to isolate the offending event:

```bash
/app/auction-projection-repair replay \
  --partition=PARTITION \
  --expected-next-offset=NEXT_OFFSET \
  --through-offset=LAST_OFFSET
```

This is a dry-run. Confirm that every `affected_lots` entry has the intended initial and expected version. A version discontinuity, frozen lot, missing `auction_lots` row, retention cliff or invalid Runtime Fact fails before any write.

## 3. Isolate the writer

Record the current replica count, then stop Projector before executing a repair:

```bash
kubectl -n live-auction get deployment projector -o jsonpath='{.spec.replicas}'
kubectl -n live-auction scale deployment/projector --replicas=0
kubectl -n live-auction wait --for=delete pod -l app.kubernetes.io/name=projector --timeout=120s
```

Stopping all Projector replicas is deliberate. It gives the operator an exclusive maintenance window and prevents an HTTP control endpoint from seeking a partition owned by another consumer. Auction commands may continue to append to Kafka, but the projection gate must stop new commands if end-to-end lag crosses its configured safety threshold.

## 4. Execute and verify

The confirmation string is derived mechanically from the preview:

```bash
/app/auction-projection-repair replay \
  --partition=PARTITION \
  --expected-next-offset=NEXT_OFFSET \
  --through-offset=LAST_OFFSET \
  --execute \
  --operator='OPERATOR_ID' \
  --reason='INCIDENT_OR_CHANGE_REFERENCE' \
  --confirm='REPLAY_PARTITION_PARTITION_FROM_NEXT_OFFSET_THROUGH_LAST_OFFSET'
```

Do not resume Projector unless the output contains all three values:

```text
"verified": true
"resume_safe": true
"restart_required": true
```

Also retain the `audit_id` in the incident record. The tool resolves only matching unresolved `RUNTIME_VERSION_GAP` findings after verification; identity conflicts and frozen lots always require separate review.

If the command fails after applying some records, leave Projector stopped, run `diagnose` again, and start the next replay from the new DB `next_offset`. Inbox and DB offset idempotency make this safe. Never reuse a stale `--expected-next-offset`.

## 5. Resume normal consumption

Restore the recorded replica count:

```bash
kubectl -n live-auction scale deployment/projector --replicas=PREVIOUS_REPLICAS
kubectl -n live-auction rollout status deployment/projector --timeout=180s
```

On assignment, `AdjustFetchOffsetsFn` seeks each partition from the MySQL DB offset. Verify:

- `/readyz` is healthy and has zero paused partitions;
- `auction_projector_paused` clears;
- Kafka consumer lag and oldest projection age converge;
- Redis/Kafka/MySQL reconciliation reports no version or canonical-hash fork;
- domain outbox backlog drains normally.

## Retention cliff and synthetic repair

Original-event replay is intentionally refused when `next_offset < Kafka earliest`. Redis current state cannot reconstruct missing bids, orders or domain-event causality. Do not force the DB cursor to Kafka earliest and do not overwrite MySQL from a Redis snapshot.

### 1. Freeze writers and protect the remaining Kafka history

Treat a retention cliff as P0. Stop all Projector replicas and close the projection gate as in section 3. Temporarily increase `auction.runtime.projection.v1` retention before preparing the evidence bundle. Record the current Kafka earliest offset, DB `next_offset`, partition and retention change in the incident ticket.

The bundle must cover exactly `[DB next_offset, Kafka earliest)` on its first run. A resumed run uses the same complete immutable bundle even when some prefix records have already committed. Do not shorten or regenerate the bundle after a partial run.

### 2. Prepare and review the evidence bundle

Create one `SyntheticRepairBundleV1` JSON document following [the approved design](superpowers/specs/2026-07-27-projector-synthetic-repair-design.md). Required controls:

- one complete Runtime Fact for every missing source offset, with no checkpoint or offset skip;
- new unique repair event IDs while preserving evidence-backed business IDs such as bid and order IDs;
- a non-empty `prepared_by`, `change_ticket`, `repair_reason` and per-record `evidence_ref`;
- deterministic protobuf bytes and their lowercase SHA-256 values;
- a preparer who will not execute the repair.

Place the file on an incident-owned read-only volume, set its mode to remove all write bits, and calculate the digest over the exact bytes:

```bash
chmod 0400 /evidence/synthetic-repair.json
sha256sum /evidence/synthetic-repair.json
```

Do not commit the bundle, place it in a ConfigMap, bake it into an image, or paste payloads/evidence references into tickets or logs. The reviewer records only the file digest and expected lot event/version/hash results.

### 3. Run the zero-write preview

Use the default preview Job in `deploy/kubernetes/operations/projection-synthetic-repair-job.example.yaml`, or run:

```bash
/app/auction-projection-repair synthetic \
  --bundle=/evidence/synthetic-repair.json \
  --expected-sha256=LOWERCASE_64_HEX_DIGEST
```

The preview must report:

- `to_offset_exclusive` equal to the current Kafka earliest offset;
- `database_offset.next_offset` inside the bundle range;
- every committed prefix record as `inbox_status=matched` and every suffix record as `inbox_status=absent`;
- the intended final event ID, version and canonical hash for every affected lot;
- `applied_records=0` and no new audit row.

Any source-position conflict, reused event ID, missing same-digest audit for a committed prefix, frozen lot, version discontinuity or changed canonical hash is a P0 stop condition.

### 4. Execute under dual control

A different engineer executes the same digest. Copy the exact confirmation value derived from the preview:

```bash
/app/auction-projection-repair synthetic \
  --bundle=/evidence/synthetic-repair.json \
  --expected-sha256=LOWERCASE_64_HEX_DIGEST \
  --execute \
  --executed-by='ENGINEER_B' \
  --confirm='SYNTHETIC_PARTITION_PARTITION_FROM_FROM_OFFSET_TO_TO_OFFSET_EXCLUSIVE_SHA256_LOWERCASE_64_HEX_DIGEST'
```

The tool reopens and revalidates the file and repeats the Kafka, DB, inbox and lot-state preflight before creating a `SYNTHETIC_REPLAY` audit. Each suffix fact then goes through the existing Projector transaction, atomically committing the business projection, inbox, domain outbox and next DB offset.

If the process stops after a prefix while Kafka earliest is unchanged, rerun the preview and execute the exact same bundle. The matching inbox prefix and prior same-digest audit are required before the tool resumes. If every Apply committed but the audit remained `STARTED`, the same bundle takes the completion-only path: it applies zero records, verifies every inbox/lot result, marks the stale audit interrupted and records a new successful attempt.

### 5. Verify and resume

Do not resume Projector unless the final report contains:

```text
"verified": true
"resume_safe": true
"restart_required": true
```

Also require `database_offset.next_offset == kafka_bounds.earliest == to_offset_exclusive`, retain the new `audit_id`, and confirm domain outbox message IDs are unique and the backlog drains. If Kafka earliest advanced during execution, the already committed prefix remains valid but `resume_safe` is false. Prepare a new independently reviewed bundle whose `from_offset` is the then-current DB `next_offset` and whose `to_offset_exclusive` is the new Kafka earliest; it must contain both any unapplied remainder and the newly expired suffix.

After success, restore Projector replicas and run all checks in section 5. Restore normal Runtime Topic retention only after lag, oldest projection age, Redis/Kafka/MySQL reconciliation and domain outbox backlog have converged.
