# ADR-2026-07-27: Accept Redis Dual-Loss Residual Window Or Switch To Kafka Command-First

## Status

Proposed for sign-off on 2026-07-27.

## Decision

Accept the residual business risk that a simultaneous loss of the Redis primary and every durable replica can still lose acknowledged runtime commands, and do not switch this release train to Kafka command-first unless the signatory rejects that window or a zero-loss objective is made mandatory.

## Scope

This acceptance applies only to the residual durability window in the current Redis Lua -> Redis List Outbox -> Kafka -> Projector pipeline.

It does not waive:

- projector pause/recovery evidence,
- Relay fencing/ownership evidence,
- MySQL/Kafka transactional correctness,
- domain relay or enrichment correctness,
- search/index correctness,
- credential handling or CI/Kubernetes obligations.

## Evidence Summary

- Local retained-repair recovery for partitions `0`, `21`, and `23` succeeded and restored DB offsets, inbox rows, lot versions, and canonical hashes.
- Local corrected `relay-kill` fault evidence passed on 2026-07-27:
  `test-results/fault-injection/impl003-relay-kill-20260727a-relay-kill`
- Local corrected `projector-kill` fault evidence passed on 2026-07-27:
  `test-results/fault-injection/impl003-projector-kill-r2-20260727a-projector-kill`
- Repository automation already proves fail-closed projection-gate semantics, Relay fencing, synthetic repair controls, and HA drill/static release gates where the current environment can execute them.
- Current local bid/search capacity reruns were blocked by runtime environment drift, not by a contradictory business invariant:
  the isolated image currently exposed no `go_build_info` metric family, and the running Gateway was configured with `AUCTION_AI_PROVIDER=deepseek` instead of the required mock mode for search-capacity evidence.

## Why This Is The Recommended Decision

- The repository already contains a coherent, heavily tested durability and recovery design around Redis runtime truth, Kafka projection, and MySQL projection/inbox enforcement.
- Switching to Kafka command-first would widen this closure task into an architectural migration across command ingress, latency semantics, replay/idempotency, frontends, deployment topology, and acceptance scope.
- The current closure objective is to complete evidence collection honestly, not to hide a known residual window.

## Residual Risk Statement

Even with `WAIT`, replica requirements, AOF, generation freeze, and bidirectional reconciliation, the system cannot prove zero command loss if the Redis primary and every durable replica are lost before the acknowledged command is relayed and projected.

## Review Trigger / Expiry

This acceptance must be reviewed at the earliest of:

- 2026-10-27,
- any business requirement that upgrades durability to zero acknowledged-command loss,
- any refusal by the reliability owner or business owner to accept the residual window,
- any production or drill evidence showing the residual window is wider than currently documented.

## Mandatory Switch Trigger

If zero acknowledged-command loss becomes a non-negotiable requirement, or if the signatory refuses to accept the residual window, stop this acceptance path and replan the system around Kafka command-first before further release sign-off.

## Consequences

- Current closure work may proceed with explicit, bounded risk ownership.
- Remote CI, Kubernetes target-environment rollout, and capacity/chaos evidence still remain required work items; this ADR does not replace them.
- Search-capacity evidence must be rerun only after the isolated image is rebuilt with the `go_build_info` compatibility patch and the Gateway runs with `AUCTION_AI_PROVIDER=mock`.

## Sign-Off

Business owner:

Name: ____________________

Title: ____________________

Signature: ____________________

Date: ____________________

Reliability owner:

Name: ____________________

Title: ____________________

Signature: ____________________

Date: ____________________
