-- KEYS:
-- 1 state, 2 ranking, 3 rankmeta, 4 participants, 5 recent, 6 idempotency,
-- 7 expiring zset, 8 outbox pending list, 9 room active lot, 10 frozen lot fence,
-- 11 room display lot.
-- ARGV:
-- 1 event_id, 2 trace_id, 3 lot_id, 4 room_id, 5 main_account_id, 6 title,
-- 7 image_url, 8 config_version, 9 previous_status, 10 previous_lot_version,
-- 11 currency, 12 start_price_fen, 13 min_increment_fen, 14 cap_price_fen or empty,
-- 15 duration_ms, 16 anti_snipe_window_ms, 17 anti_snipe_extend_ms,
-- 18 max_extend_count, 19 live_status, 20 command_type, 21 schema_version,
-- 22 max_fact_bytes.

local state_key = KEYS[1]
local ranking_key = KEYS[2]
local rankmeta_key = KEYS[3]
local participants_key = KEYS[4]
local recent_key = KEYS[5]
local idempotency_key = KEYS[6]
local expiring_key = KEYS[7]
local outbox_key = KEYS[8]
local room_active_key = KEYS[9]
local frozen_lot_key = KEYS[10]
local room_display_key = KEYS[11]

local max_exact_integer = 9007199254740991

local function exact_nonnegative(value)
  return value ~= nil and value >= 0 and value <= max_exact_integer and value == math.floor(value)
end

local function exact_positive(value)
  return value ~= nil and value > 0 and value <= max_exact_integer and value == math.floor(value)
end

local function valid_currency(value)
  return type(value) == 'string' and string.match(value, '^[A-Z][A-Z][A-Z]$') ~= nil
end

local function valid_uuid_v7(value)
  if type(value) ~= 'string' or #value ~= 36 then
    return false
  end
  if string.sub(value, 9, 9) ~= '-' or string.sub(value, 14, 14) ~= '-' or string.sub(value, 19, 19) ~= '-' or string.sub(value, 24, 24) ~= '-' then
    return false
  end
  if string.sub(value, 15, 15) ~= '7' or string.match(string.sub(value, 20, 20), '^[89ab]$') == nil then
    return false
  end
  return string.match(value, '^[0-9a-f]+%-[0-9a-f]+%-[0-9a-f]+%-[0-9a-f]+%-[0-9a-f]+$') ~= nil
end

local function reject(code)
  return cjson.encode({ ok = false, code = code, message = code })
end

-- PHASE: READ
local state_exists = redis.call('EXISTS', state_key)
local active_lot_id = redis.call('GET', room_active_key) or ''
local lot_frozen = redis.call('EXISTS', frozen_lot_key)
local expiring_type = redis.call('TYPE', expiring_key).ok
local outbox_type = redis.call('TYPE', outbox_key).ok
local redis_time = redis.call('TIME')

-- PHASE: VALIDATE_AND_SERIALIZE
local event_id = ARGV[1]
local trace_id = ARGV[2]
local lot_id = ARGV[3]
local room_id = ARGV[4]
local main_account_id = ARGV[5]
local title = ARGV[6]
local image_url = ARGV[7]
local config_version = tonumber(ARGV[8])
local previous_status = tonumber(ARGV[9])
local previous_lot_version = tonumber(ARGV[10])
local currency = ARGV[11]
local start_price_fen = tonumber(ARGV[12])
local min_increment_fen = tonumber(ARGV[13])
local cap_price_text = ARGV[14]
local cap_price_fen = nil
if cap_price_text ~= '' then
  cap_price_fen = tonumber(cap_price_text)
end
local duration_ms = tonumber(ARGV[15])
local anti_snipe_window_ms = tonumber(ARGV[16])
local anti_snipe_extend_ms = tonumber(ARGV[17])
local max_extend_count = tonumber(ARGV[18])
local live_status = tonumber(ARGV[19])
local command_type = tonumber(ARGV[20])
local schema_version = tonumber(ARGV[21])
local max_fact_bytes = tonumber(ARGV[22])
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)

if lot_frozen == 1 then
  return reject('LOT_FROZEN')
end
if (expiring_type ~= 'none' and expiring_type ~= 'zset') or (outbox_type ~= 'none' and outbox_type ~= 'list') then
  return reject('RUNTIME_STATE_CORRUPT')
end
if state_exists ~= 0 then
  return reject('RUNTIME_STATE_ALREADY_EXISTS')
end
if active_lot_id ~= '' and active_lot_id ~= lot_id then
  return reject('ROOM_HAS_ACTIVE_LOT')
end
if not valid_uuid_v7(event_id) or lot_id == '' or room_id == '' or main_account_id == '' or title == '' then
  return reject('INVALID_ARGUMENT')
end
if previous_status ~= 1 and previous_status ~= 6 then
  return reject('RUNTIME_STATE_ALREADY_EXISTS')
end
if not exact_positive(config_version) or not exact_nonnegative(previous_lot_version) or previous_lot_version == max_exact_integer then
  return reject('INVALID_ARGUMENT')
end
if not valid_currency(currency) or not exact_nonnegative(start_price_fen) or not exact_positive(min_increment_fen) or start_price_fen > max_exact_integer - min_increment_fen then
  return reject('INVALID_ARGUMENT')
end
if cap_price_fen ~= nil and (not exact_nonnegative(cap_price_fen) or cap_price_fen < start_price_fen) then
  return reject('INVALID_ARGUMENT')
end
if not exact_positive(duration_ms) or not exact_nonnegative(anti_snipe_window_ms) or not exact_nonnegative(anti_snipe_extend_ms) or not exact_nonnegative(max_extend_count) or max_extend_count > 2147483647 then
  return reject('INVALID_ARGUMENT')
end
if not exact_positive(now_ms) or now_ms > max_exact_integer - duration_ms or live_status ~= 2 or command_type ~= 1 or schema_version ~= 1 or not exact_positive(max_fact_bytes) then
  return reject('INVALID_ARGUMENT')
end

local lot_version = previous_lot_version + 1
local ends_at_unix_ms = now_ms + duration_ms
local state_after = {
  lot_id = lot_id,
  room_id = room_id,
  status = live_status,
  currency = currency,
  start_price_fen = start_price_fen,
  min_increment_fen = min_increment_fen,
  current_price_fen = start_price_fen,
  started_at_unix_ms = now_ms,
  ends_at_unix_ms = ends_at_unix_ms,
  bid_count = 0,
  participant_count = 0,
  extend_count = 0,
  max_extend_count = max_extend_count,
  duration_ms = duration_ms,
  anti_snipe_window_ms = anti_snipe_window_ms,
  anti_snipe_extend_ms = anti_snipe_extend_ms
}
if cap_price_fen ~= nil then
  state_after.cap_price_fen = cap_price_fen
end
local runtime_fact = {
  event_id = event_id,
  trace_id = trace_id,
  lot_id = lot_id,
  room_id = room_id,
  prev_lot_version = previous_lot_version,
  lot_version = lot_version,
  occurred_at_unix_ms = now_ms,
  config_version = config_version,
  command = command_type,
  state_after = state_after,
  schema_version = schema_version
}
local state_after_payload = cjson.encode(state_after)
local fact_payload = cjson.encode(runtime_fact)
if #fact_payload > max_fact_bytes then
  return reject('RUNTIME_FACT_TOO_LARGE')
end
local outbox_item = event_id .. '\n' .. fact_payload
local response_payload = cjson.encode({
  ok = true,
  event_id = event_id,
  lot_version = lot_version,
  occurred_at_unix_ms = now_ms,
  fact_json = fact_payload
})

-- PHASE: WRITE
redis.call('DEL', ranking_key, rankmeta_key, participants_key, recent_key, idempotency_key)
redis.call('HSET', state_key,
  'lot_id', lot_id,
  'room_id', room_id,
  'main_account_id', main_account_id,
  'title', title,
  'image_url', image_url,
  'config_version', config_version,
  'currency', currency,
  'start_price_fen', start_price_fen,
  'min_increment_fen', min_increment_fen,
  'cap_price_fen', cap_price_text,
  'duration_ms', duration_ms,
  'anti_snipe_window_ms', anti_snipe_window_ms,
  'anti_snipe_extend_ms', anti_snipe_extend_ms,
  'max_extend_count', max_extend_count,
  'status', live_status,
  'version', lot_version,
  'last_event_id', event_id,
  'state_after_json', state_after_payload,
  'current_price_fen', start_price_fen,
  'leading_user_id', '',
  'leading_nickname', '',
  'winner_user_id', '',
  'winner_nickname', '',
  'final_price_fen', 0,
  'started_at_unix_ms', now_ms,
  'ends_at_unix_ms', ends_at_unix_ms,
  'settled_at_unix_ms', 0,
  'cancelled_at_unix_ms', 0,
  'cancel_reason', '',
  'bid_count', 0,
  'participant_count', 0,
  'extend_count', 0,
  'order_id', '')
redis.call('SET', room_active_key, lot_id)
redis.call('SET', room_display_key, lot_id)
redis.call('ZADD', expiring_key, ends_at_unix_ms, lot_id)
redis.call('LPUSH', outbox_key, outbox_item)
return response_payload
