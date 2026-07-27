# Search rebuild runbook

`auction-search-rebuild` rebuilds a new Elasticsearch or pgvector generation. It captures the `auction.lot.state.v1` high watermarks, scans a MySQL `REPEATABLE READ` snapshot by primary key, replays Kafka from those watermarks, verifies count and sampled `lot_version + last_event_id + content_hash`, then atomically publishes the new generation.

## Preconditions

- `domain-relay`, the selected index consumer, Kafka, MySQL and the selected search store are healthy.
- Every queued/live/extended MySQL lot has a current `auction.lot.state.v1` row in `auction_domain_outbox`.
- The rebuild principal has only `Read` and `Describe` on `auction.lot.state.v1`.
- Pick a new monotonically increasing target: `auction-lots-v2` for ES or `auction_lot_search_docs_v2` for pgvector. Never use the live alias/table as a target.

## Validate only

```bash
docker compose --profile search-ops run --rm search-rebuild-es --dry-run
```

This performs no Elasticsearch writes. A missing current event for a public lot or a MySQL/event content fork fails the run.

Estimate pgvector paid work without writing or calling the embedding provider:

```bash
docker compose --profile search-ops run --rm search-rebuild-pgvector \
  --sink=pgvector --dry-run
```

The final log reports `reusable_embeddings` and `new_embeddings_required`. The model name, model version and dimensions must match the intended write run or the estimate is invalid.

## Build and switch

```bash
docker compose --profile search-ops run --rm search-rebuild-es \
  --target=auction-lots-v2 \
  --mapping=/app/elasticsearch/index-v1.json \
  --page-size=500 \
  --max-documents=1000000
```

`--max-documents` is an optional guard. Omit it for unlimited scanning. Use `--switch-alias=false` to build and validate the target without exposing it.

Build pgvector with a hard cap on newly paid embedding documents:

```bash
docker compose --profile search-ops run --rm search-rebuild-pgvector \
  --sink=pgvector \
  --target=auction_lot_search_docs_v2 \
  --max-new-embeddings=5000 \
  --page-size=500 \
  --max-documents=1000000
```

The cap is reserved before each provider call, so the command can never intentionally issue more paid requests than allowed. `0` permits only exact-hash reuse. Provider billing remains authoritative; this guard counts documents, not tokens or currency. Use `--switch-table=false` to build and validate without promotion.

Set `AUCTION_EMBEDDING_COST_PER_MILLION_TOKENS` to the provider's current input-token price in the billing currency used by the team. Successful provider responses contribute their reported input-token usage to `auction_embedding_tokens_estimate_total`; providers that omit usage fall back to a conservative UTF-8 character count and use `source="estimated"`. `auction_embedding_cost_estimate_total` multiplies that usage by the configured price. Both metrics reset with the process and are operational estimates only: the provider invoice remains authoritative. Put the actual budget threshold in the platform alert configuration because currency and budget differ by environment.

## Pause and resume

Stop the one-shot process with `SIGTERM`. The live alias remains unchanged unless the switch had already completed. Resume the same target explicitly:

```bash
docker compose --profile search-ops run --rm search-rebuild-es \
  --target=auction-lots-v2 \
  --mapping=/app/elasticsearch/index-v1.json \
  --resume
```

Snapshot and Kafka records are safe to replay because the target uses strict external `lot_version`. Equal-version writes must have the same event ID and content hash.

Resume pgvector with the same model identity, target and a deliberate remaining cap:

```bash
docker compose --profile search-ops run --rm search-rebuild-pgvector \
  --sink=pgvector \
  --target=auction_lot_search_docs_v2 \
  --max-new-embeddings=1000 \
  --resume
```

Successfully stored target vectors are detected before another provider call. A changed model/version/dimension produces a different embedding hash and therefore consumes the new run's cap.

## After switching

Keep the previous index for at least 24 hours. Confirm gateway search errors, fallback rate and `index-es` lag remain normal, take an SLM snapshot, then delete the old index manually. The rebuild command never deletes old data.

For pgvector, the previous canonical table is retained as `auction_lot_search_docs_backup_YYYYMMDD_HHMMSS`. Confirm vector lag, semantic-search errors and sampled identities for at least 24 hours, take a PostgreSQL backup, then drop that table manually. The command never drops it.
