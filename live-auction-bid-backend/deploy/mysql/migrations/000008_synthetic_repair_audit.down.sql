ALTER TABLE auction_projection_repair_audit
  DROP CHECK chk_projection_repair_synthetic_metadata,
  DROP CHECK chk_projection_repair_record_count,
  DROP CHECK chk_projection_repair_type,
  DROP COLUMN record_count,
  DROP COLUMN change_ticket,
  DROP COLUMN prepared_by,
  DROP COLUMN bundle_sha256;
