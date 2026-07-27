ALTER TABLE auction_room_states
  ADD COLUMN display_lot_id VARCHAR(64) NOT NULL DEFAULT '' AFTER active_lot_id;

UPDATE auction_room_states
SET display_lot_id = active_lot_id
WHERE display_lot_id = '' AND active_lot_id <> '';
