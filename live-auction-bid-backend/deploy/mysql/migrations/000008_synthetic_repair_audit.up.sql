ALTER TABLE auction_projection_repair_audit
  ADD COLUMN bundle_sha256 CHAR(64) NULL AFTER repair_reason,
  ADD COLUMN prepared_by VARCHAR(128) NULL AFTER bundle_sha256,
  ADD COLUMN change_ticket VARCHAR(128) NULL AFTER prepared_by,
  ADD COLUMN record_count INT NOT NULL DEFAULT 0 AFTER change_ticket,
  ADD CONSTRAINT chk_projection_repair_type
    CHECK (repair_type IN ('ORIGINAL_REPLAY', 'SYNTHETIC_REPLAY')),
  ADD CONSTRAINT chk_projection_repair_record_count
    CHECK (record_count >= 0),
  ADD CONSTRAINT chk_projection_repair_synthetic_metadata
    CHECK (
      repair_type <> 'SYNTHETIC_REPLAY'
      OR (
        bundle_sha256 IS NOT NULL
        AND bundle_sha256 REGEXP '^[0-9a-f]{64}$'
        AND prepared_by IS NOT NULL AND prepared_by <> ''
        AND change_ticket IS NOT NULL AND change_ticket <> ''
        AND record_count > 0
      )
    );
