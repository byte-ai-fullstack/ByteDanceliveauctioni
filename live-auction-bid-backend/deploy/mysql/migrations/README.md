# MySQL schema migrations

The paired `NNNNNN_name.up.sql` and `NNNNNN_name.down.sql` files are the only executable source of truth for the target MySQL schema. The migration binary embeds only those paired files; the older date-prefixed SQL files are historical upgrade aids and are not loaded.

## Commands

Run flags before the command name:

```bash
export AUCTION_MYSQL_DSN='auction:password@tcp(127.0.0.1:3306)/live_auction?charset=utf8mb4'

go run ./app/auction/service/cmd/migrate up
go run ./app/auction/service/cmd/migrate status
go run ./app/auction/service/cmd/migrate --steps 1 down
```

Each run:

- reserves one physical MySQL connection;
- acquires `GET_LOCK('live_auction_schema_migrate', 30)` on that connection;
- validates the stored SHA-256 checksum before applying or reverting anything;
- records applied versions in `auction_schema_migrations`;
- releases the advisory lock on the same connection.

`docker compose up` runs `auction-migrate up` as a one-shot dependency before application services. Application processes use a read-only verifier against `auction_schema_migrations`; they fail fast on missing, unknown, non-contiguous, or checksum-drifted versions and never create the version table themselves.

MySQL DDL is not fully transactional. Keep migrations small, test both directions against a fresh MySQL 8.4 database, and stop for manual inspection if a DDL statement fails midway. Never edit an applied migration; add a new version.

## Target-schema rules

- This schema is for a clean rebuild, as required by `docs/REFACTORING_PLAN.md`; it is not an in-place legacy-volume upgrader.
- Money columns are integer minor units. The lot owns its `CHAR(3)` currency and orders retain a currency snapshot.
- Obsolete projection-offset tables and delivery bookkeeping columns are intentionally absent.
- `auction_projection_inbox` is the message-idempotency authority; `auction_projection_partition_offsets` is the projector recovery authority.
- Every executing Projector replay attempt is recorded in `auction_projection_repair_audit`; operators must never repair offsets with ad-hoc SQL.
- Application services must not call `AutoMigrate` after the target-schema cutover. Schema changes belong in a new migration pair.
