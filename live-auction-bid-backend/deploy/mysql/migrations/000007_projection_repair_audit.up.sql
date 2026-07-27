CREATE TABLE auction_projection_repair_audit (
  repair_id             VARCHAR(64)  NOT NULL,
  repair_type           VARCHAR(32)  NOT NULL,
  topic                 VARCHAR(128) NOT NULL,
  kafka_partition       INT          NOT NULL,
  from_offset           BIGINT       NOT NULL,
  to_offset_exclusive   BIGINT       NOT NULL,
  operator_id           VARCHAR(128) NOT NULL,
  repair_reason         VARCHAR(512) NOT NULL,
  status                VARCHAR(16)  NOT NULL,
  detail_json           JSON         NOT NULL,
  created_at_ms         BIGINT       NOT NULL,
  completed_at_ms       BIGINT       NOT NULL DEFAULT 0,
  PRIMARY KEY (repair_id),
  KEY idx_projection_repair_partition
    (topic, kafka_partition, created_at_ms),
  CONSTRAINT chk_projection_repair_offsets
    CHECK (kafka_partition >= 0 AND from_offset >= 0 AND to_offset_exclusive > from_offset),
  CONSTRAINT chk_projection_repair_status
    CHECK (status IN ('STARTED', 'SUCCEEDED', 'FAILED')),
  CONSTRAINT chk_projection_repair_completion
    CHECK ((status = 'STARTED' AND completed_at_ms = 0) OR (status <> 'STARTED' AND completed_at_ms > 0))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
