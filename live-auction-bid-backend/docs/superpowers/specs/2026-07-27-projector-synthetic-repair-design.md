# Projector Synthetic Repair Design

Date: 2026-07-27

## 1. Purpose

This design closes the Projector recovery case where MySQL
`auction_projection_partition_offsets.next_offset` is lower than Kafka's
earliest retained offset. The original Runtime Facts no longer exist in Kafka,
so the normal `auction-projection-repair replay` path cannot recover them.

Synthetic repair is a P0 break-glass operation. It reconstructs every missing
Runtime Fact from a human-reviewed evidence bundle and applies those facts
through the existing Projector transaction. It must not infer facts from the
current Redis aggregate, silently skip Kafka offsets, or create a second
projection implementation.

## 2. Fixed decisions

1. The evidence source is an operator-prepared bundle. Redis may be used only
   for comparison; it cannot generate repair facts automatically.
2. Every missing Kafka offset has exactly one complete Runtime Fact. Aggregate
   or per-lot checkpoints are not allowed.
3. The bundle preparer and executor are different people. A change or incident
   reference is mandatory.
4. The tool applies the facts directly through `projector.SQLStore.Apply`. It
   does not append synthetic positions to the Runtime Topic and does not add a
   Repair Topic.
5. Every fact has a new globally unique event ID. Business identities that the
   missing fact originally created, such as `bid_id` and `order_id`, remain the
   evidence-backed original identities.
6. A first synthetic attempt is available only when
   `DB next_offset < Kafka earliest`. If the original records remain in Kafka,
   the original replay path is mandatory. A completion-only retry may run when
   `DB next_offset == Kafka earliest` only if every bundle record already has a
   matching inbox row and a prior audit references the same bundle digest.

## 3. Evidence bundle

### 3.1 File handling

The input is one read-only `SyntheticRepairBundleV1` JSON document. The tool:

- opens it with no-follow semantics and verifies with `fstat` that it is a
  regular file;
- rejects files larger than 384 MiB;
- computes SHA-256 over the exact file bytes;
- requires `--expected-sha256` to match that digest;
- rejects unknown fields, duplicate object keys, invalid UTF-8 and trailing
  content;
- streams the records instead of retaining decoded payloads for all records;
- reopens and re-hashes the file immediately before execution so a replaced
  file cannot pass an earlier preview.

Production bundles are mounted from an incident-owned read-only volume. They
must not be committed to Git, copied into the application image, placed in a
normal ConfigMap, or written to application logs.

### 3.2 Schema

```json
{
  "schema_version": 1,
  "topic": "auction.runtime.projection.v1",
  "partition": 7,
  "from_offset": 120,
  "to_offset_exclusive": 124,
  "prepared_by": "engineer-a",
  "change_ticket": "INC-2026-0042",
  "repair_reason": "restore retained-out runtime facts",
  "created_at_unix_ms": 1785081600000,
  "records": [
    {
      "source_offset": 120,
      "repair_event_id": "019f...",
      "owner_epoch": 31,
      "outbox_shard": 4,
      "runtime_fact_base64": "...",
      "payload_sha256": "...",
      "evidence_ref": "INC-2026-0042/bid-log-000120"
    }
  ]
}
```

Top-level constraints:

- `schema_version` is exactly `1`.
- `topic` is exactly `auction.runtime.projection.v1`.
- `partition` is non-negative.
- `[from_offset, to_offset_exclusive)` is non-empty and contains at most 1000
  offsets.
- The number of records equals the offset range length.
- `prepared_by`, `change_ticket` and `repair_reason` are non-empty, bounded and
  free of control characters.
- `created_at_unix_ms` is positive and cannot be unreasonably in the future.

Record constraints:

- `source_offset` values are strictly contiguous from `from_offset`.
- `runtime_fact_base64` decodes to no more than
  `eventcontract.MaxRuntimeFactBytes`.
- `payload_sha256` matches the decoded bytes.
- The protobuf passes `eventcontract.ValidateRuntimeFact` and deterministic
  payload validation.
- `repair_event_id` equals `RuntimeFactV1.event_id`, is unique within the
  bundle, and is absent from the Projector inbox before first application.
- `owner_epoch` is positive and `outbox_shard` is within the fixed Runtime
  Outbox shard range.
- `evidence_ref` is non-empty and identifies the incident evidence used to
  reconstruct this fact.

The command never prints `runtime_fact_base64`, accepted-bid personal fields,
order drafts or raw evidence references. Reports contain only offsets, event
IDs, lot IDs, versions, hashes and aggregate counts.

## 4. Command contract

The existing binary gains a `synthetic` subcommand:

```text
auction-projection-repair synthetic \
  --bundle=/evidence/synthetic-repair.json \
  --expected-sha256=<64 lowercase hex> \
  [--execute \
   --executed-by=<identity> \
   --confirm=<exact confirmation>]
```

Without `--execute`, the command is a strict zero-write preview. The exact
confirmation contains the partition, range and complete bundle SHA-256:

```text
SYNTHETIC_PARTITION_<p>_FROM_<from>_TO_<to-exclusive>_SHA256_<digest>
```

`executed_by` must be non-empty and different from `prepared_by`. The bundle
itself supplies the approved reason and change ticket, so they cannot be
changed at execution time without changing the reviewed SHA-256.

## 5. Preflight algorithm

1. Read Kafka bounds and the MySQL partition offset without initializing it.
2. Require `DB next_offset < Kafka earliest`. The only exception is a
   completion-only retry at equality with a full matching inbox prefix and a
   prior `STARTED` or `FAILED` audit for the same bundle digest and range.
3. Require `bundle.to_offset_exclusive == Kafka earliest` and
   `bundle.from_offset <= DB next_offset`.
4. Verify the complete file schema, digest, record count, contiguous offsets,
   Runtime Fact contracts, payload hashes and per-lot version chains.
5. For bundle offsets lower than the current DB offset, require an inbox row
   with the same source position, repair event ID, lot/version and payload
   hash. These rows are an already-applied prefix from an interrupted attempt.
6. For offsets at or above the current DB offset, require each repair event ID
   to be absent from the inbox. A reused event ID or source-position conflict
   fails preflight.
7. Load the current `auction_lot_projection_state` and `auction_lots.version`
   for every suffix lot. The first suffix fact must start from that version;
   every later fact for the lot must be continuous.
8. Compute the expected final event ID, lot version and canonical hash for every
   affected lot. The preview prints these values and the number of prefix and
   suffix records.

No audit row or business row is written during preview.

## 6. Execution algorithm

1. Reopen the bundle and repeat the exact SHA-256 and schema checks.
2. Re-read Kafka earliest and MySQL next offset. Any change invalidates the
   preview and execution stops before the first write.
3. Insert a `SYNTHETIC_REPLAY` row in
   `auction_projection_repair_audit` with status `STARTED`, the bundle digest,
   preparer, executor, change ticket, reason, range and record count.
4. Skip only the prefix already proven by matching inbox identities.
5. For each suffix record, build a `projector.DecodedRecord` using the fixed
   Runtime Topic, partition, missing source offset, decoded payload,
   `owner_epoch` and `outbox_shard`, then call `projector.SQLStore.Apply`.
6. Require each call to return exactly `source_offset + 1`. An
   `AlreadyAdvanced` result indicates concurrent movement and stops the run.
7. After the last record, require MySQL next offset to equal the bundle end.
8. Re-read Kafka bounds. Its earliest offset must still equal the bundle end.
9. Reload every affected lot and verify `last_event_id`,
   `last_lot_version`, `auction_lots.version` and `canonical_hash` against the
   bundle-derived final state.
10. Mark the audit `SUCCEEDED`, resolve only matching range-scoped
    `RUNTIME_VERSION_GAP` findings, and return `resume_safe=true`.

Each `Apply` call retains the existing transaction boundary: business
projection, Projector inbox, domain outbox and DB next offset commit together.
Derived domain messages use the repair event ID as causation, so interrupted
and resumed runs cannot duplicate their message IDs.

## 7. Interruption and conflicts

The repair is intentionally not one large database transaction. A 1000-record
transaction would hold locks too long and make failure recovery worse.

If record N fails or the process stops:

- records before N remain committed;
- the attempt audit becomes `FAILED` when the process can still report the
  failure; a hard kill may leave `STARTED`, which the next run reports as a
  stale attempt;
- the same immutable bundle may be run again;
- the preflight verifies every committed prefix inbox row and resumes from the
  current DB offset;
- a bundle with a different hash cannot replace the committed prefix;
- a source-position, event-ID, payload, lot-version or canonical-hash conflict
  is P0 and never auto-unfreezes a lot.

If a hard kill happens after the last `Apply` commit but before audit
completion, a completion-only retry verifies all inbox rows and final lot
states, terminalizes the stale `STARTED` audit as failed/interrupted, writes a
new successful zero-apply attempt, and returns `resume_safe=true`. Equality
between DB next offset and Kafka earliest is never sufficient on its own.

If Kafka earliest advances during execution, applied records remain valid but
the attempt cannot return `resume_safe`. The operator must prepare a new bundle
starting at the then-current DB offset and ending at the new Kafka earliest;
it covers any unapplied remainder plus the newly expired suffix without
replacing the already committed prefix. The runbook therefore requires
temporarily increasing Runtime Topic retention before creating the bundle.

## 8. Audit schema

A forward migration extends `auction_projection_repair_audit` with nullable
synthetic metadata:

- `bundle_sha256 CHAR(64)`;
- `prepared_by VARCHAR(128)`;
- `change_ticket VARCHAR(128)`;
- `record_count INT`.

Original replay rows leave these fields null or zero. Synthetic replay requires
all of them. The existing `operator_id` stores the executor and
`repair_reason` stores the bundle reason. `detail_json` contains preview and
verification summaries but never raw protobufs or evidence contents.

## 9. Operations

1. Confirm the partition is paused because of a retention cliff.
2. Stop all Projector replicas and close the projection gate.
3. Temporarily increase Runtime Topic retention and verify headroom.
4. Prepare and independently review the evidence bundle.
5. Run the synthetic preview and record the exact digest and expected final
   lot states.
6. A different operator executes the confirmed bundle.
7. Resume Projector only when the report contains `verified=true` and
   `resume_safe=true`.
8. Confirm readiness, lag convergence, Redis/Kafka/MySQL reconciliation and
   domain outbox drain before restoring normal retention.

The Kubernetes example Job remains read-only by default. Production execution
mounts the incident bundle read-only and uses the dedicated
`projection-repair` Kafka principal, which has only Runtime Topic `Read` and
`Describe` ACLs and no consumer-group or Kafka write permission.

## 10. Verification

Unit tests must cover:

- no-follow/regular-file checks, file and record size limits;
- unknown and duplicate JSON fields, malformed base64/protobuf and hash
  mismatches;
- dual-control identity and exact confirmation;
- missing, duplicate and out-of-order offsets;
- duplicate repair event IDs and existing inbox conflicts;
- per-lot version gaps, frozen lots and canonical hash derivation;
- zero-write preview;
- partial application followed by same-bundle resume;
- changed-bundle rejection after a committed prefix;
- Kafka earliest movement before and after execution;
- final offset and lot-state verification.

MySQL integration tests must prove:

- each synthetic fact atomically commits business rows, inbox, domain outbox
  and the DB offset;
- a failure at record N leaves no partial effects for N;
- rerunning the same bundle does not duplicate bids, orders or domain messages;
- audit states and range-scoped finding resolution are correct;
- migration up/down includes the synthetic audit fields.

A test-environment failure drill kills the repair process after a random
record, reruns the same bundle and proves:

- DB next offset equals Kafka earliest;
- every affected lot matches the expected event ID/version/hash;
- domain outbox message IDs are unique;
- the normal Projector can resume and consume the first retained Kafka record.

## 11. Non-goals

- Reconstructing facts automatically from Redis current state.
- Skipping a range with one aggregate checkpoint.
- Rewriting Kafka offsets or consumer-group commits.
- Publishing synthetic facts to the Runtime Topic or a new repair topic.
- Repairing identity conflicts or unfreezing lots automatically.
- Archiving production evidence bundles inside this repository.
