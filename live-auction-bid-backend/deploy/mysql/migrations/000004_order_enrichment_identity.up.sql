ALTER TABLE auction_order_enrichments
  CHANGE COLUMN source_event_id source_message_id VARCHAR(128) NOT NULL,
  ADD COLUMN payload_hash CHAR(64) NULL AFTER source_message_id,
  DROP INDEX uk_source_event,
  ADD UNIQUE KEY uk_source_message (source_message_id);

UPDATE auction_order_enrichments
SET payload_hash = SHA2(CONCAT('legacy:', source_message_id), 256)
WHERE payload_hash IS NULL OR payload_hash = '';

ALTER TABLE auction_order_enrichments
  MODIFY COLUMN payload_hash CHAR(64) NOT NULL;
