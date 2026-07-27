-- KEYS: 1 state, 2 ranking, 3 rankmeta, 4 participants, 5 outbox pending list, 6 frozen lot fence.
-- ARGV:
-- 1 event_id, 2 trace_id, 3 expected_config_version, 4 next_config_version,
-- 5 lot_id, 6 room_id, 7 main_account_id, 8 title, 9 image_url, 10 currency,
-- 11 start_price_fen, 12 min_increment_fen, 13 cap_price_fen or empty,
-- 14 duration_ms, 15 anti_snipe_window_ms, 16 anti_snipe_extend_ms,
-- 17 max_extend_count, 18 command_type, 19 schema_version, 20 max_fact_bytes,
-- 21 ranking_limit.

local state_key = KEYS[1]
local ranking_key = KEYS[2]
local rankmeta_key = KEYS[3]
local participants_key = KEYS[4]
local outbox_key = KEYS[5]
local frozen_lot_key = KEYS[6]

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
local values = redis.call('HMGET', state_key,
  'lot_id', 'room_id', 'main_account_id', 'title', 'image_url', 'config_version', 'currency',
  'status', 'version', 'current_price_fen', 'leading_user_id', 'leading_nickname',
  'winner_user_id', 'winner_nickname', 'final_price_fen', 'started_at_unix_ms',
  'ends_at_unix_ms', 'settled_at_unix_ms', 'cancelled_at_unix_ms', 'cancel_reason',
  'bid_count', 'extend_count', 'order_id')
local participant_count_raw = redis.call('SCARD', participants_key)
local lot_frozen = redis.call('EXISTS', frozen_lot_key)
local outbox_type = redis.call('TYPE', outbox_key).ok
local ranking_limit_for_read = tonumber(ARGV[21])
local ranking_rows = redis.call('ZREVRANGE', ranking_key, 0, ranking_limit_for_read - 1, 'WITHSCORES')
local ranking_user_ids = {}
for index = 1, #ranking_rows, 2 do
  table.insert(ranking_user_ids, ranking_rows[index])
end
local ranking_meta_rows = {}
if #ranking_user_ids > 0 then
  ranking_meta_rows = redis.call('HMGET', rankmeta_key, unpack(ranking_user_ids))
end
local redis_time = redis.call('TIME')

-- PHASE: VALIDATE_AND_SERIALIZE
local event_id = ARGV[1]
local trace_id = ARGV[2]
local expected_config_version = tonumber(ARGV[3])
local next_config_version = tonumber(ARGV[4])
local next_lot_id = ARGV[5]
local next_room_id = ARGV[6]
local next_main_account_id = ARGV[7]
local next_title = ARGV[8]
local next_image_url = ARGV[9]
local next_currency = ARGV[10]
local next_start_price_fen = tonumber(ARGV[11])
local next_min_increment_fen = tonumber(ARGV[12])
local next_cap_price_text = ARGV[13]
local next_cap_price_fen = nil
if next_cap_price_text ~= '' then
  next_cap_price_fen = tonumber(next_cap_price_text)
end
local next_duration_ms = tonumber(ARGV[14])
local next_anti_snipe_window_ms = tonumber(ARGV[15])
local next_anti_snipe_extend_ms = tonumber(ARGV[16])
local next_max_extend_count = tonumber(ARGV[17])
local command_type = tonumber(ARGV[18])
local schema_version = tonumber(ARGV[19])
local max_fact_bytes = tonumber(ARGV[20])
local ranking_limit = tonumber(ARGV[21])
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)

if lot_frozen == 1 then
  return reject('LOT_FROZEN')
end
if outbox_type ~= 'none' and outbox_type ~= 'list' then
  return reject('RUNTIME_STATE_CORRUPT')
end
local lot_id = values[1] or ''
local room_id = values[2] or ''
local main_account_id = values[3] or ''
local current_title = values[4] or ''
local current_image_url = values[5] or ''
local current_config_version = tonumber(values[6])
local current_currency = values[7] or ''
local status = tonumber(values[8])
local previous_lot_version = tonumber(values[9])
local current_price_fen = tonumber(values[10])
local leading_user_id = values[11] or ''
local leading_nickname = values[12] or ''
local winner_user_id = values[13] or ''
local winner_nickname = values[14] or ''
local final_price_fen = tonumber(values[15])
local started_at_unix_ms = tonumber(values[16])
local ends_at_unix_ms = tonumber(values[17])
local settled_at_unix_ms = tonumber(values[18])
local cancelled_at_unix_ms = tonumber(values[19])
local cancel_reason = values[20] or ''
local bid_count = tonumber(values[21])
local extend_count = tonumber(values[22])
local order_id = values[23] or ''
local participant_count = tonumber(participant_count_raw)

if lot_id == '' then
  return reject('RUNTIME_STATE_MISSING')
end
if not valid_uuid_v7(event_id) or next_lot_id == '' or next_room_id == '' or next_main_account_id == '' or next_title == '' then
  return reject('INVALID_ARGUMENT')
end
if status == 3 or status == 4 or status == 8 then
  return reject('CONFIG_FROZEN')
end
if status ~= 1 and status ~= 2 and status ~= 5 and status ~= 6 and status ~= 7 then
  return reject('RUNTIME_STATE_CORRUPT')
end
if not exact_positive(current_config_version) or not exact_positive(expected_config_version) or expected_config_version ~= current_config_version then
  return reject('CONFIG_VERSION_CONFLICT')
end
if current_config_version == max_exact_integer or not exact_positive(next_config_version) or next_config_version ~= current_config_version + 1 then
  return reject('CONFIG_VERSION_CONFLICT')
end
if next_lot_id ~= lot_id or next_room_id ~= room_id or next_main_account_id ~= main_account_id or next_title ~= current_title or next_image_url ~= current_image_url or next_currency ~= current_currency then
  return reject('CONFIG_VERSION_CONFLICT')
end
if not valid_currency(next_currency) or not exact_nonnegative(next_start_price_fen) or not exact_positive(next_min_increment_fen) or next_start_price_fen > max_exact_integer - next_min_increment_fen then
  return reject('INVALID_ARGUMENT')
end
if next_cap_price_fen ~= nil and (not exact_nonnegative(next_cap_price_fen) or next_cap_price_fen < next_start_price_fen) then
  return reject('INVALID_ARGUMENT')
end
if not exact_positive(next_duration_ms) or not exact_nonnegative(next_anti_snipe_window_ms) or not exact_nonnegative(next_anti_snipe_extend_ms) or not exact_nonnegative(next_max_extend_count) or next_max_extend_count > 2147483647 then
  return reject('INVALID_ARGUMENT')
end
if not exact_nonnegative(previous_lot_version) or previous_lot_version == max_exact_integer or not exact_nonnegative(current_price_fen) or not exact_nonnegative(final_price_fen) or not exact_nonnegative(started_at_unix_ms) or not exact_nonnegative(ends_at_unix_ms) or not exact_nonnegative(settled_at_unix_ms) or not exact_nonnegative(cancelled_at_unix_ms) or not exact_nonnegative(bid_count) or not exact_nonnegative(participant_count) or not exact_nonnegative(extend_count) or extend_count > 2147483647 then
  return reject('RUNTIME_STATE_CORRUPT')
end
if next_max_extend_count < extend_count or (next_cap_price_fen ~= nil and next_cap_price_fen <= current_price_fen) then
  return reject('CONFIG_FROZEN')
end
if not exact_positive(now_ms) or command_type ~= 5 or schema_version ~= 1 or not exact_positive(max_fact_bytes) or not exact_positive(ranking_limit) or ranking_limit > 100 then
  return reject('INVALID_ARGUMENT')
end

local ranking_top = {}
for index = 1, #ranking_user_ids do
  local raw_meta = ranking_meta_rows[index]
  if raw_meta == false or raw_meta == nil then
    return reject('RUNTIME_STATE_CORRUPT')
  end
  local decoded_ok, meta = pcall(cjson.decode, raw_meta)
  local amount_fen = tonumber(ranking_rows[index * 2])
  if not decoded_ok or type(meta) ~= 'table' or not exact_nonnegative(amount_fen) or not exact_positive(tonumber(meta.bid_at_unix_ms)) then
    return reject('RUNTIME_STATE_CORRUPT')
  end
  table.insert(ranking_top, {
    rank = index,
    user_id = ranking_user_ids[index],
    masked_nickname = meta.masked_nickname or '',
    avatar_url = meta.avatar_url or '',
    amount_fen = amount_fen,
    bid_at_unix_ms = tonumber(meta.bid_at_unix_ms)
  })
end

local lot_version = previous_lot_version + 1
local state_after = {
  lot_id = lot_id,
  room_id = room_id,
  status = status,
  currency = next_currency,
  start_price_fen = next_start_price_fen,
  min_increment_fen = next_min_increment_fen,
  current_price_fen = current_price_fen,
  leading_user_id = leading_user_id,
  leading_nickname = leading_nickname,
  winner_user_id = winner_user_id,
  winner_nickname = winner_nickname,
  final_price_fen = final_price_fen,
  started_at_unix_ms = started_at_unix_ms,
  ends_at_unix_ms = ends_at_unix_ms,
  settled_at_unix_ms = settled_at_unix_ms,
  cancelled_at_unix_ms = cancelled_at_unix_ms,
  cancel_reason = cancel_reason,
  bid_count = bid_count,
  participant_count = participant_count,
  extend_count = extend_count,
  max_extend_count = next_max_extend_count,
  order_id = order_id,
  duration_ms = next_duration_ms,
  anti_snipe_window_ms = next_anti_snipe_window_ms,
  anti_snipe_extend_ms = next_anti_snipe_extend_ms
}
if next_cap_price_fen ~= nil then
  state_after.cap_price_fen = next_cap_price_fen
end
if #ranking_top > 0 then
  state_after.top_ranking = ranking_top
end
local runtime_fact = {
  event_id = event_id,
  trace_id = trace_id,
  lot_id = lot_id,
  room_id = room_id,
  prev_lot_version = previous_lot_version,
  lot_version = lot_version,
  occurred_at_unix_ms = now_ms,
  config_version = next_config_version,
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
redis.call('HSET', state_key,
  'title', next_title,
  'image_url', next_image_url,
  'config_version', next_config_version,
  'start_price_fen', next_start_price_fen,
  'min_increment_fen', next_min_increment_fen,
  'cap_price_fen', next_cap_price_text,
  'duration_ms', next_duration_ms,
  'anti_snipe_window_ms', next_anti_snipe_window_ms,
  'anti_snipe_extend_ms', next_anti_snipe_extend_ms,
  'max_extend_count', next_max_extend_count,
  'version', lot_version,
  'last_event_id', event_id,
  'state_after_json', state_after_payload)
redis.call('LPUSH', outbox_key, outbox_item)
return response_payload
