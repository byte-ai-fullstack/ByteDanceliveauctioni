ALTER TABLE auction_order_enrichments
  DROP INDEX uk_source_message,
  DROP COLUMN payload_hash,
  CHANGE COLUMN source_message_id source_event_id VARCHAR(64) NOT NULL,
  ADD UNIQUE KEY uk_source_event (source_event_id);
