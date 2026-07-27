CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS auction_lot_search_docs (
  lot_id VARCHAR(64) PRIMARY KEY,
  room_id VARCHAR(64) NOT NULL,
  main_account_id VARCHAR(64) NOT NULL,
  title TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  category VARCHAR(64) NOT NULL DEFAULT '',
  tags JSONB NOT NULL DEFAULT '[]'::jsonb,
  image_url TEXT NOT NULL DEFAULT '',
  search_text TEXT NOT NULL,
  status VARCHAR(64) NOT NULL,
  start_price_fen BIGINT NOT NULL DEFAULT 0,
  current_price_fen BIGINT NOT NULL DEFAULT 0,
  currency CHAR(3) NOT NULL DEFAULT 'CNY',
  starts_at_unix_ms BIGINT NOT NULL DEFAULT 0,
  ends_at_unix_ms BIGINT NOT NULL DEFAULT 0,
  href TEXT NOT NULL,
  public_visible BOOLEAN NOT NULL DEFAULT FALSE,
  lot_version BIGINT NOT NULL DEFAULT 0,
  last_event_id VARCHAR(64) NOT NULL DEFAULT '',
  content_hash CHAR(64) NOT NULL DEFAULT '',
  embedding_provider VARCHAR(64) NOT NULL DEFAULT 'dashscope',
  embedding_model VARCHAR(128) NOT NULL,
  embedding_model_version VARCHAR(128) NOT NULL DEFAULT '',
  embedding_dimensions INTEGER NOT NULL,
  embedding_hash CHAR(64) NOT NULL,
  embedding vector,
  indexed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  lot_updated_at_unix_ms BIGINT NOT NULL DEFAULT 0,
  current_price_amount BIGINT NOT NULL DEFAULT 0,
  current_price_currency VARCHAR(16) NOT NULL DEFAULT ''
);

ALTER TABLE auction_lot_search_docs
  ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS category VARCHAR(64) NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS tags JSONB NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN IF NOT EXISTS image_url TEXT NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS start_price_fen BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS current_price_fen BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS currency CHAR(3) NOT NULL DEFAULT 'CNY',
  ADD COLUMN IF NOT EXISTS starts_at_unix_ms BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS ends_at_unix_ms BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS lot_version BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS last_event_id VARCHAR(64) NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS content_hash CHAR(64) NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS embedding_provider VARCHAR(64) NOT NULL DEFAULT 'dashscope',
  ADD COLUMN IF NOT EXISTS embedding_model_version VARCHAR(128) NOT NULL DEFAULT '';

ALTER TABLE auction_lot_search_docs
  ALTER COLUMN embedding TYPE vector USING embedding::vector;

UPDATE auction_lot_search_docs
SET current_price_fen = current_price_amount,
    currency = CASE
      WHEN current_price_currency ~ '^[A-Z]{3}$' THEN current_price_currency
      ELSE 'CNY'
    END,
    embedding_model_version = CASE
      WHEN embedding_model_version = '' THEN embedding_model
      ELSE embedding_model_version
    END
WHERE lot_version = 0 AND last_event_id = '';

CREATE INDEX IF NOT EXISTS idx_lot_search_public_status
  ON auction_lot_search_docs (public_visible, status);
CREATE INDEX IF NOT EXISTS idx_lot_search_room
  ON auction_lot_search_docs (room_id);
CREATE INDEX IF NOT EXISTS idx_lot_search_version
  ON auction_lot_search_docs (lot_version);
CREATE INDEX IF NOT EXISTS idx_lot_search_hidden_indexed_at
  ON auction_lot_search_docs (indexed_at) WHERE public_visible = FALSE;
