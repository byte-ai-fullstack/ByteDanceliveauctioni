CREATE TABLE auction_projection_inbox (
  event_id          VARCHAR(64)  NOT NULL,
  topic             VARCHAR(128) NOT NULL,
  kafka_partition   INT          NOT NULL,
  kafka_offset      BIGINT       NOT NULL,
  lot_id            VARCHAR(64)  NOT NULL,
  lot_version       BIGINT       NOT NULL,
  payload_hash      CHAR(64)     NOT NULL,
  applied_at_ms     BIGINT       NOT NULL,
  PRIMARY KEY (event_id),
  UNIQUE KEY uk_source_position (topic, kafka_partition, kafka_offset),
  KEY idx_lot_version (lot_id, lot_version),
  KEY idx_applied_at (applied_at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE auction_lot_projection_state (
  lot_id             VARCHAR(64) NOT NULL,
  room_id            VARCHAR(64) NOT NULL,
  last_event_id      VARCHAR(64) NULL,
  last_lot_version   BIGINT      NOT NULL DEFAULT 0,
  canonical_hash     CHAR(32)    NOT NULL DEFAULT '',
  frozen             TINYINT     NOT NULL DEFAULT 0,
  last_applied_ms    BIGINT      NOT NULL DEFAULT 0,
  PRIMARY KEY (lot_id),
  KEY idx_room (room_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE auction_projection_partition_offsets (
  topic              VARCHAR(128) NOT NULL,
  kafka_partition    INT          NOT NULL,
  next_offset        BIGINT       NOT NULL,
  updated_at_ms      BIGINT       NOT NULL,
  PRIMARY KEY (topic, kafka_partition)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE auction_domain_outbox (
  id                 BIGINT       NOT NULL AUTO_INCREMENT,
  message_id         VARCHAR(128) NOT NULL,
  causation_id       VARCHAR(64)  NOT NULL,
  topic              VARCHAR(128) NOT NULL,
  partition_key      VARCHAR(64)  NOT NULL,
  payload            MEDIUMBLOB   NOT NULL,
  headers_json       JSON         NULL,
  created_at_ms      BIGINT       NOT NULL,
  published_at_ms    BIGINT       NOT NULL DEFAULT 0,
  attempts           INT          NOT NULL DEFAULT 0,
  next_attempt_ms    BIGINT       NOT NULL DEFAULT 0,
  locked_by          VARCHAR(64)  NOT NULL DEFAULT '',
  lock_token         VARCHAR(64)  NOT NULL DEFAULT '',
  locked_until_ms    BIGINT       NOT NULL DEFAULT 0,
  last_error         VARCHAR(512) NOT NULL DEFAULT '',
  PRIMARY KEY (id),
  UNIQUE KEY uk_message_id (message_id),
  KEY idx_causation (causation_id),
  KEY idx_claimable (published_at_ms, next_attempt_ms, locked_until_ms, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE auction_order_enrichments (
  order_id           VARCHAR(64)  NOT NULL,
  source_event_id    VARCHAR(64)  NOT NULL,
  address_snapshot   JSON         NULL,
  shop_snapshot      JSON         NULL,
  status             VARCHAR(16)  NOT NULL,
  attempts           INT          NOT NULL DEFAULT 0,
  last_error         VARCHAR(512) NOT NULL DEFAULT '',
  updated_at_ms      BIGINT       NOT NULL,
  PRIMARY KEY (order_id),
  UNIQUE KEY uk_source_event (source_event_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE auction_reconcile_findings (
  id                 BIGINT       NOT NULL AUTO_INCREMENT,
  kind               VARCHAR(32)  NOT NULL,
  lot_id             VARCHAR(64)  NOT NULL,
  severity           VARCHAR(8)   NOT NULL,
  detail_json        JSON         NOT NULL,
  detected_at_ms     BIGINT       NOT NULL,
  resolved_at_ms     BIGINT       NOT NULL DEFAULT 0,
  resolution_json    JSON         NULL,
  PRIMARY KEY (id),
  KEY idx_unresolved (severity, resolved_at_ms, detected_at_ms),
  KEY idx_lot (lot_id, detected_at_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
