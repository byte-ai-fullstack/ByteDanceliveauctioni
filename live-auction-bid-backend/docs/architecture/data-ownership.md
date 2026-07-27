# Target data ownership

Every mutable record has one owning component. Other components may read it but must not silently create a second write path.

| Data | Sole writer | Allowed non-owner behavior |
|---|---|---|
| Redis lot runtime state, ranking and expiry set | Auction Lua commands | Gateway and workers may read snapshots or scan candidates |
| Redis pending/inflight outbox and shard lease | Lua command plus outbox relay ACK/lease scripts | Health controller may inspect backlog |
| `auction_lots` runtime columns | Projector | Auction service may edit pre-live configuration through the configuration use case |
| `auction_bids`, auction order core and projection state/inbox/offset | Projector | APIs and reconcilers are read-only |
| `auction_domain_outbox` | Projector inserts; domain relay claims and marks publication | Other consumers are read-only |
| `auction_order_enrichments` | Enrichment consumer | Query layer joins the snapshot without updating the order core |
| `auction_reconcile_findings` | Reconciler | Operators may resolve a finding through the audited resolution path |
| Elasticsearch lot documents | ES index consumer | Search API is read-only and never treats indexed price as authoritative |
| pgvector lot embeddings | pgvector index consumer | Semantic search reads and then batch-hydrates authoritative fields |

The Kafka projector transaction owns the atomic boundary between business projection rows, `auction_projection_inbox`, derived `auction_domain_outbox` rows, and `auction_projection_partition_offsets`. No remote call, embedding request, NATS publication, or Kafka publication is allowed inside that transaction.
