CREATE TABLE auction_rooms (
  id VARCHAR(64) PRIMARY KEY,
  main_account_id VARCHAR(64) NOT NULL,
  name VARCHAR(128) NOT NULL,
  platform VARCHAR(32) NOT NULL DEFAULT 'douyin',
  platform_room_id VARCHAR(128) NOT NULL DEFAULT '',
  live_source_url VARCHAR(512) NOT NULL DEFAULT '',
  live_started_at_unix_ms BIGINT NOT NULL DEFAULT 0,
  status VARCHAR(32) NOT NULL DEFAULT 'ACTIVE',
  created_by_user_id VARCHAR(64) NOT NULL DEFAULT '',
  created_at_unix_ms BIGINT NOT NULL,
  updated_at_unix_ms BIGINT NOT NULL,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  UNIQUE KEY uidx_room_main_account (main_account_id),
  KEY idx_room_main_status (main_account_id, status),
  KEY idx_room_created_by (created_by_user_id),
  KEY idx_platform_room (platform_room_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE auction_room_states (
  room_id VARCHAR(64) PRIMARY KEY,
  main_account_id VARCHAR(64) NOT NULL,
  active_lot_id VARCHAR(64) NOT NULL DEFAULT '',
  active_lot_version BIGINT NOT NULL DEFAULT 0,
  next_queue_position INT NOT NULL DEFAULT 1,
  updated_at_unix_ms BIGINT NOT NULL,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  KEY idx_room_state_main (main_account_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE auction_lots (
  id VARCHAR(64) PRIMARY KEY,
  main_account_id VARCHAR(64) NOT NULL,
  room_id VARCHAR(64) NOT NULL,
  title VARCHAR(255) NOT NULL,
  description TEXT NOT NULL,
  image_url VARCHAR(1024) NOT NULL,
  status INT NOT NULL,
  queue_status INT NOT NULL DEFAULT 1,
  queue_position INT NOT NULL DEFAULT 0,
  currency CHAR(3) NOT NULL DEFAULT 'CNY',
  start_price_amount BIGINT NOT NULL,
  min_increment_amount BIGINT NOT NULL,
  cap_price_amount BIGINT NULL,
  duration_seconds INT NOT NULL,
  anti_snipe_window_seconds INT NOT NULL,
  anti_snipe_extend_seconds INT NOT NULL,
  max_extend_count INT NOT NULL,
  current_price_amount BIGINT NOT NULL,
  leading_user_id VARCHAR(64) NOT NULL DEFAULT '',
  leading_nickname VARCHAR(128) NOT NULL DEFAULT '',
  started_at_unix_ms BIGINT NOT NULL DEFAULT 0,
  ends_at_unix_ms BIGINT NOT NULL DEFAULT 0,
  settled_at_unix_ms BIGINT NOT NULL DEFAULT 0,
  cancel_reason VARCHAR(512) NOT NULL DEFAULT '',
  cancelled_at_unix_ms BIGINT NOT NULL DEFAULT 0,
  winner_user_id VARCHAR(64) NOT NULL DEFAULT '',
  winner_nickname VARCHAR(128) NOT NULL DEFAULT '',
  final_price_amount BIGINT NOT NULL DEFAULT 0,
  version BIGINT NOT NULL,
  config_version BIGINT NOT NULL DEFAULT 1,
  playbook_stage INT NOT NULL,
  payload JSON NOT NULL,
  active_room_key VARCHAR(64) GENERATED ALWAYS AS (
    CASE WHEN status IN (2, 7) THEN room_id ELSE NULL END
  ) STORED,
  queued_room_position_key VARCHAR(96) GENERATED ALWAYS AS (
    CASE
      WHEN queue_status IN (2, 3) AND queue_position > 0 THEN CONCAT(room_id, '#', queue_position)
      ELSE NULL
    END
  ) STORED,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  KEY idx_lot_main_room_status (main_account_id, room_id, status),
  KEY idx_lot_main_room_queue (main_account_id, room_id, queue_status, queue_position),
  KEY idx_lot_main_updated (main_account_id, updated_at),
  KEY idx_room_status (room_id, status),
  KEY idx_room_queue (room_id, queue_status, queue_position),
  KEY idx_room_updated (room_id, updated_at),
  KEY idx_status_ends_at (status, ends_at_unix_ms),
  UNIQUE KEY uidx_one_active_lot_per_room (active_room_key),
  UNIQUE KEY uidx_one_queued_position_per_room (queued_room_position_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE auction_bids (
  id VARCHAR(64) PRIMARY KEY,
  main_account_id VARCHAR(64) NOT NULL,
  lot_id VARCHAR(64) NOT NULL,
  user_id VARCHAR(64) NOT NULL,
  nickname VARCHAR(128) NOT NULL,
  amount BIGINT NOT NULL,
  idempotency_key VARCHAR(128) NOT NULL,
  created_at_unix_ms BIGINT NOT NULL,
  payload JSON NOT NULL,
  created_at DATETIME(3) NULL,
  KEY idx_bid_main_lot_created (main_account_id, lot_id, created_at_unix_ms),
  KEY idx_lot_created (lot_id, created_at_unix_ms),
  KEY idx_lot_amount (lot_id, amount),
  KEY idx_lot_user (lot_id, user_id),
  UNIQUE KEY idx_lot_user_idem (lot_id, user_id, idempotency_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE auction_lot_stats (
  lot_id VARCHAR(64) PRIMARY KEY,
  main_account_id VARCHAR(64) NOT NULL,
  room_id VARCHAR(64) NOT NULL,
  bid_count BIGINT NOT NULL DEFAULT 0,
  participant_count BIGINT NOT NULL DEFAULT 0,
  last_bid_id VARCHAR(64) NOT NULL DEFAULT '',
  last_bid_at_unix_ms BIGINT NOT NULL DEFAULT 0,
  projected_version BIGINT NOT NULL DEFAULT 0,
  updated_at_unix_ms BIGINT NOT NULL,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  KEY idx_lot_stats_room (room_id),
  KEY idx_lot_stats_main_room (main_account_id, room_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE auction_lot_participants (
  lot_id VARCHAR(64) NOT NULL,
  user_id VARCHAR(64) NOT NULL,
  main_account_id VARCHAR(64) NOT NULL,
  room_id VARCHAR(64) NOT NULL,
  first_bid_id VARCHAR(64) NOT NULL,
  first_bid_at_unix_ms BIGINT NOT NULL,
  created_at DATETIME(3) NULL,
  PRIMARY KEY (lot_id, user_id),
  KEY idx_lot_participants_room (room_id),
  KEY idx_lot_participants_main_room (main_account_id, room_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE auction_events (
  id VARCHAR(64) PRIMARY KEY,
  main_account_id VARCHAR(64) NOT NULL,
  room_id VARCHAR(64) NOT NULL,
  lot_id VARCHAR(64) NOT NULL DEFAULT '',
  type INT NOT NULL,
  occurred_at_unix_ms BIGINT NOT NULL,
  reason VARCHAR(512) NOT NULL DEFAULT '',
  payload JSON NOT NULL,
  created_at DATETIME(3) NULL,
  KEY idx_event_main_room_occurred (main_account_id, room_id, occurred_at_unix_ms),
  KEY idx_room_occurred (room_id, occurred_at_unix_ms),
  KEY idx_lot_occurred (lot_id, occurred_at_unix_ms),
  KEY idx_type_occurred (type, occurred_at_unix_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE user_orders (
  id VARCHAR(64) PRIMARY KEY,
  source VARCHAR(32) NOT NULL,
  source_order_id VARCHAR(64) NOT NULL,
  order_no VARCHAR(64) NOT NULL DEFAULT '',
  main_account_id VARCHAR(64) NOT NULL DEFAULT '',
  user_id VARCHAR(64) NOT NULL,
  nickname VARCHAR(128) NOT NULL DEFAULT '',
  status VARCHAR(32) NOT NULL,
  payment_status VARCHAR(32) NOT NULL,
  payment_id VARCHAR(64) NOT NULL DEFAULT '',
  title VARCHAR(255) NOT NULL DEFAULT '',
  shop_name VARCHAR(128) NOT NULL DEFAULT '',
  total_amount BIGINT NOT NULL,
  currency CHAR(3) NOT NULL DEFAULT 'CNY',
  shipping_address_id VARCHAR(64) NOT NULL DEFAULT '',
  shipping_address_snapshot JSON NULL,
  address_snapshot VARCHAR(512) NOT NULL DEFAULT '',
  created_at_unix_ms BIGINT NOT NULL,
  updated_at_unix_ms BIGINT NOT NULL,
  paid_at_unix_ms BIGINT NOT NULL DEFAULT 0,
  expires_at_unix_ms BIGINT NOT NULL DEFAULT 0,
  version BIGINT NOT NULL DEFAULT 1,
  payment_idempotency_key VARCHAR(128) NOT NULL DEFAULT '',
  source_payload JSON NULL,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  UNIQUE KEY uk_user_order_source_id (source, source_order_id),
  KEY idx_user_order_user_status (user_id, status, source),
  KEY idx_user_order_payment_status (payment_status),
  KEY idx_user_order_no (order_no),
  KEY idx_user_order_main_created (main_account_id, created_at_unix_ms),
  KEY idx_user_order_source_created (source, created_at_unix_ms),
  KEY idx_user_order_shipping_address (shipping_address_id),
  KEY idx_user_order_created (created_at_unix_ms),
  KEY idx_user_order_expiry (expires_at_unix_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE user_order_items (
  id VARCHAR(64) PRIMARY KEY,
  order_id VARCHAR(64) NOT NULL,
  source VARCHAR(32) NOT NULL,
  source_item_id VARCHAR(64) NOT NULL DEFAULT '',
  product_id VARCHAR(64) NOT NULL DEFAULT '',
  sku_id VARCHAR(64) NOT NULL DEFAULT '',
  lot_id VARCHAR(64) NOT NULL DEFAULT '',
  room_id VARCHAR(64) NOT NULL DEFAULT '',
  title VARCHAR(255) NOT NULL DEFAULT '',
  image_url VARCHAR(1024) NOT NULL DEFAULT '',
  sku_name VARCHAR(128) NOT NULL DEFAULT '',
  quantity BIGINT NOT NULL DEFAULT 1,
  unit_amount BIGINT NOT NULL,
  total_amount BIGINT NOT NULL,
  currency CHAR(3) NOT NULL DEFAULT 'CNY',
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  KEY idx_user_order_item_order (order_id),
  KEY idx_user_order_item_source (source),
  KEY idx_user_order_item_product (product_id),
  KEY idx_user_order_item_lot (lot_id),
  KEY idx_user_order_item_room (room_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE user_order_payments (
  id VARCHAR(64) PRIMARY KEY,
  order_id VARCHAR(64) NOT NULL,
  source VARCHAR(32) NOT NULL,
  provider VARCHAR(32) NOT NULL DEFAULT 'mock',
  main_account_id VARCHAR(64) NOT NULL DEFAULT '',
  lot_id VARCHAR(64) NOT NULL DEFAULT '',
  user_id VARCHAR(64) NOT NULL,
  status VARCHAR(32) NOT NULL,
  amount BIGINT NOT NULL,
  currency CHAR(3) NOT NULL DEFAULT 'CNY',
  idempotency_key VARCHAR(128) NOT NULL,
  created_at_unix_ms BIGINT NOT NULL,
  updated_at_unix_ms BIGINT NOT NULL DEFAULT 0,
  succeeded_at_unix_ms BIGINT NOT NULL DEFAULT 0,
  source_payload JSON NULL,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  UNIQUE KEY uk_user_order_payment_idem (order_id, idempotency_key),
  KEY idx_user_order_payment_order (order_id),
  KEY idx_user_order_payment_source (source),
  KEY idx_user_order_payment_main_created (main_account_id, created_at_unix_ms),
  KEY idx_user_order_payment_lot (lot_id),
  KEY idx_user_order_payment_user (user_id),
  KEY idx_user_order_payment_status (status),
  KEY idx_user_order_payment_created (created_at_unix_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE auction_deposit_holds (
  id VARCHAR(64) PRIMARY KEY,
  main_account_id VARCHAR(64) NOT NULL,
  room_id VARCHAR(64) NOT NULL,
  lot_id VARCHAR(64) NOT NULL,
  buyer_user_id VARCHAR(64) NOT NULL,
  buyer_nickname VARCHAR(128) NOT NULL DEFAULT '',
  status VARCHAR(32) NOT NULL,
  amount BIGINT NOT NULL,
  currency CHAR(3) NOT NULL DEFAULT 'CNY',
  payment_provider VARCHAR(32) NOT NULL DEFAULT 'mock',
  payment_id VARCHAR(64) NOT NULL DEFAULT '',
  idempotency_key VARCHAR(128) NOT NULL,
  address_id VARCHAR(64) NOT NULL DEFAULT '',
  address_snapshot JSON NULL,
  created_at_unix_ms BIGINT NOT NULL,
  updated_at_unix_ms BIGINT NOT NULL,
  held_at_unix_ms BIGINT NOT NULL DEFAULT 0,
  released_at_unix_ms BIGINT NOT NULL DEFAULT 0,
  payload JSON NOT NULL,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  UNIQUE KEY uk_deposit_lot_buyer (lot_id, buyer_user_id),
  UNIQUE KEY uk_deposit_idem (lot_id, buyer_user_id, idempotency_key),
  KEY idx_deposit_main_created (main_account_id, created_at_unix_ms),
  KEY idx_deposit_room (room_id),
  KEY idx_deposit_lot_status (lot_id, status),
  KEY idx_deposit_buyer_status (buyer_user_id, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE auction_users (
  id VARCHAR(64) PRIMARY KEY,
  username VARCHAR(64) NOT NULL,
  nickname VARCHAR(128) NOT NULL,
  avatar_url VARCHAR(512) NOT NULL DEFAULT '',
  password_hash VARCHAR(255) NOT NULL,
  main_account_id VARCHAR(64) NOT NULL DEFAULT '',
  created_by_user_id VARCHAR(64) NOT NULL DEFAULT '',
  status INT NOT NULL DEFAULT 1,
  created_at_unix_ms BIGINT NOT NULL,
  updated_at_unix_ms BIGINT NOT NULL,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  UNIQUE KEY idx_username (username),
  KEY idx_user_main_status (main_account_id, status),
  KEY idx_user_created_by (created_by_user_id),
  KEY idx_user_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE auction_roles (
  code VARCHAR(64) PRIMARY KEY,
  name VARCHAR(128) NOT NULL,
  description VARCHAR(512) NOT NULL DEFAULT '',
  `system` BOOLEAN NOT NULL DEFAULT TRUE,
  created_at_unix_ms BIGINT NOT NULL,
  updated_at_unix_ms BIGINT NOT NULL,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE auction_permissions (
  code VARCHAR(96) PRIMARY KEY,
  name VARCHAR(128) NOT NULL,
  module VARCHAR(64) NOT NULL,
  description VARCHAR(512) NOT NULL DEFAULT '',
  created_at_unix_ms BIGINT NOT NULL,
  updated_at_unix_ms BIGINT NOT NULL,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  KEY idx_permission_module (module)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE auction_user_roles (
  id VARCHAR(64) PRIMARY KEY,
  user_id VARCHAR(64) NOT NULL,
  role_code VARCHAR(64) NOT NULL,
  main_account_id VARCHAR(64) NOT NULL DEFAULT '',
  granted_by_user_id VARCHAR(64) NOT NULL DEFAULT '',
  created_at_unix_ms BIGINT NOT NULL,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  UNIQUE KEY uk_user_role_scope (user_id, role_code, main_account_id),
  KEY idx_user_role_user (user_id),
  KEY idx_user_role_role (role_code),
  KEY idx_user_role_main (main_account_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE auction_role_permissions (
  id VARCHAR(64) PRIMARY KEY,
  role_code VARCHAR(64) NOT NULL,
  permission_code VARCHAR(96) NOT NULL,
  created_at_unix_ms BIGINT NOT NULL,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  UNIQUE KEY uk_role_permission (role_code, permission_code),
  KEY idx_role_permission_role (role_code),
  KEY idx_role_permission_permission (permission_code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE auction_user_permissions (
  id VARCHAR(64) PRIMARY KEY,
  user_id VARCHAR(64) NOT NULL,
  permission_code VARCHAR(96) NOT NULL,
  effect VARCHAR(16) NOT NULL DEFAULT 'allow',
  granted_by_user_id VARCHAR(64) NOT NULL DEFAULT '',
  created_at_unix_ms BIGINT NOT NULL,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  UNIQUE KEY uk_user_permission (user_id, permission_code),
  KEY idx_user_permission_user (user_id),
  KEY idx_user_permission_permission (permission_code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE auction_user_sessions (
  id VARCHAR(64) PRIMARY KEY,
  user_id VARCHAR(64) NOT NULL,
  refresh_token_hash VARCHAR(64) NOT NULL,
  refresh_expires_at_unix_ms BIGINT NOT NULL,
  revoked_at_unix_ms BIGINT NOT NULL DEFAULT 0,
  created_at_unix_ms BIGINT NOT NULL,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  KEY idx_user_sessions (user_id),
  UNIQUE KEY idx_refresh_token_hash (refresh_token_hash),
  KEY idx_session_expiry (refresh_expires_at_unix_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE asset_files (
  id VARCHAR(64) PRIMARY KEY,
  main_account_id VARCHAR(64) NOT NULL DEFAULT '',
  owner_user_id VARCHAR(64) NOT NULL,
  room_id VARCHAR(64) NOT NULL DEFAULT '',
  biz_type VARCHAR(64) NOT NULL,
  status VARCHAR(32) NOT NULL DEFAULT 'temporary',
  attached_lot_id VARCHAR(64) NOT NULL DEFAULT '',
  storage_provider VARCHAR(32) NOT NULL,
  bucket VARCHAR(128) NOT NULL,
  object_key VARCHAR(512) NOT NULL,
  public_url VARCHAR(1024) NOT NULL,
  original_name VARCHAR(255) NOT NULL DEFAULT '',
  mime_type VARCHAR(64) NOT NULL,
  size_bytes BIGINT NOT NULL,
  sha256 CHAR(64) NOT NULL,
  attached_at_unix_ms BIGINT NOT NULL DEFAULT 0,
  deleted_at_unix_ms BIGINT NOT NULL DEFAULT 0,
  expires_at_unix_ms BIGINT NOT NULL DEFAULT 0,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  KEY idx_asset_main_room (main_account_id, room_id),
  KEY idx_asset_owner (owner_user_id),
  KEY idx_asset_room (room_id),
  KEY idx_asset_biz_type (biz_type),
  KEY idx_asset_status (status),
  KEY idx_asset_attached_lot (attached_lot_id),
  KEY idx_asset_sha256 (sha256),
  KEY idx_asset_expiry (expires_at_unix_ms),
  KEY idx_asset_deleted_at (deleted_at_unix_ms),
  UNIQUE KEY idx_asset_object_key (object_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE shop_products (
  id VARCHAR(64) PRIMARY KEY,
  title VARCHAR(255) NOT NULL,
  subtitle VARCHAR(512) NOT NULL DEFAULT '',
  description TEXT NOT NULL,
  category VARCHAR(64) NOT NULL,
  shop_name VARCHAR(128) NOT NULL,
  main_image_url VARCHAR(1024) NOT NULL,
  detail_image_urls JSON NOT NULL,
  tags JSON NOT NULL,
  badges JSON NOT NULL,
  price_amount BIGINT NOT NULL,
  original_price_amount BIGINT NOT NULL DEFAULT 0,
  currency CHAR(3) NOT NULL DEFAULT 'CNY',
  sold_label VARCHAR(64) NOT NULL DEFAULT '',
  live BOOLEAN NOT NULL DEFAULT FALSE,
  status VARCHAR(32) NOT NULL DEFAULT 'active',
  created_at_unix_ms BIGINT NOT NULL,
  updated_at_unix_ms BIGINT NOT NULL,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  KEY idx_shop_product_category (category),
  KEY idx_shop_product_status (status),
  KEY idx_shop_product_updated (updated_at_unix_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE shop_skus (
  id VARCHAR(64) PRIMARY KEY,
  product_id VARCHAR(64) NOT NULL,
  name VARCHAR(128) NOT NULL,
  price_amount BIGINT NOT NULL,
  currency CHAR(3) NOT NULL DEFAULT 'CNY',
  stock BIGINT NOT NULL DEFAULT 0,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  KEY idx_shop_sku_product (product_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE user_delivery_addresses (
  id VARCHAR(64) PRIMARY KEY,
  user_id VARCHAR(64) NOT NULL,
  receiver_name VARCHAR(64) NOT NULL,
  phone VARCHAR(32) NOT NULL,
  province VARCHAR(64) NOT NULL DEFAULT '',
  city VARCHAR(64) NOT NULL DEFAULT '',
  district VARCHAR(64) NOT NULL DEFAULT '',
  street VARCHAR(128) NOT NULL DEFAULT '',
  detail VARCHAR(512) NOT NULL,
  postal_code VARCHAR(32) NOT NULL DEFAULT '',
  tag VARCHAR(32) NOT NULL DEFAULT '',
  is_default BOOLEAN NOT NULL DEFAULT FALSE,
  status VARCHAR(32) NOT NULL DEFAULT 'active',
  created_at_unix_ms BIGINT NOT NULL,
  updated_at_unix_ms BIGINT NOT NULL,
  deleted_at_unix_ms BIGINT NOT NULL DEFAULT 0,
  default_user_key VARCHAR(64) GENERATED ALWAYS AS (
    CASE WHEN status = 'active' AND is_default THEN user_id ELSE NULL END
  ) STORED,
  created_at DATETIME(3) NULL,
  updated_at DATETIME(3) NULL,
  UNIQUE KEY uk_one_default_address_per_user (default_user_key),
  KEY idx_user_address_active (user_id, status, updated_at_unix_ms)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
