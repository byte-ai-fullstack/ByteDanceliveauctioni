# Search reconciliation runbook

`auction-search-reconciler` continuously compares the authoritative MySQL lot-state event identity with Elasticsearch and pgvector. It scans a bounded primary-key page per interval, wraps at the end, and never reads current price from either search index as truth.

## Outcomes

For each sink, it compares `lot_version + last_event_id + content_hash`:

- `healthy`: exact identity match; no write.
- `missing` / `incomplete` / `stale`: publish the already validated original `auction.lot.state.v1` protobuf again. For pgvector, a visible document without its compatible embedding is `incomplete`. The independent ES and pgvector consumer groups apply or skip using their own version rules.
- `conflict`: equal version with a different event ID or content hash. Record a `SEARCH_ES_CONFLICT` or `SEARCH_VECTOR_CONFLICT` P0 finding; never overwrite.
- `ahead`: the index version exceeds the authoritative MySQL/outbox snapshot. Record a P0 finding; never roll the index back.
- `error`: that sink could not be read. The other sink is still checked and can still trigger a repair.

Repair publication uses Kafka `acks=all` and the exact protobuf identity stored in MySQL Domain Outbox. The worker has `Write` only on `auction.lot.state.v1`, owns no consumer group, and does not write either index directly.

## Start and verify

```bash
docker compose --profile search up -d search-reconciler
curl -fsS http://127.0.0.1:18090/readyz
curl -fsS http://127.0.0.1:18090/metrics | grep auction_search_reconcile
```

The in-memory cursor advances only after a complete page succeeds. A restart begins at the first lot again; duplicate repair records are safe. Tune `AUCTION_SEARCH_RECONCILE_PAGE_SIZE`, bounded `AUCTION_SEARCH_RECONCILE_CONCURRENCY`, and `AUCTION_SEARCH_RECONCILE_INTERVAL` so a full wrap completes inside the drift-detection SLO without loading MySQL or either index.

## Respond to alerts

- `AuctionSearchRepairFailure`: verify Kafka ISR/ACLs and the `search-reconciler` producer principal. The same page is retried.
- `AuctionSearchReconcileUnavailableSink`: inspect the failing sink label and its independent index consumer lag.
- `AuctionSearchReconcileIdentityConflict`: stop manual repair attempts and inspect the unresolved finding plus source Domain Outbox payload. Equal/higher versions are deliberately never overwritten.
- `AuctionSearchReconcileStalled`: inspect MySQL snapshot latency, sink timeouts, finding writes and Kafka acknowledgements.

Resolve a P0 finding only after preserving evidence and proving the source payload, affected version and repaired sink identity. Do not set `resolved_at_ms` merely to silence the alert.
