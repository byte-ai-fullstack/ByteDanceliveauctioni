CREATE TABLE auction_lot_presentations (
  lot_id VARCHAR(64) NOT NULL,
  main_account_id VARCHAR(64) NOT NULL,
  version BIGINT NOT NULL,
  payload JSON NOT NULL,
  updated_at_unix_ms BIGINT NOT NULL,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  PRIMARY KEY (lot_id),
  KEY idx_lot_presentation_main (main_account_id),
  CONSTRAINT chk_lot_presentation_version CHECK (version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
