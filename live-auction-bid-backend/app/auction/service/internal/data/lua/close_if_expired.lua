-- KEYS:
-- 1 state, 2 ranking, 3 rankmeta, 4 participants, 5 recent, 6 idempotency,
-- 7 expiring zset, 8 outbox pending list, 9 room active lot, 10 frozen lot fence,
-- 11 room display lot.
-- ARGV:
-- 1 event_id, 2 trace_id, 3 order_id, 4 settled_status, 5 failed_status,
-- 6 command_type, 7 schema_version, 8 terminal_ttl_seconds, 9 max_fact_bytes,
-- 10 ranking_limit, 11 no_bid_reason.

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

local function reject(code, ends_at_unix_ms)
  local response = { ok = false, code = code, message = code }
  if ends_at_unix_ms ~= nil then
    response.ends_at_unix_ms = ends_at_unix_ms
  end
  return cjson.encode(response)
end

-- PHASE: READ
local values = redis.call('HMGET', state_key,
  'lot_id', 'room_id', 'main_account_id', 'title', 'image_url',
  'config_version', 'currency', 'start_price_fen', 'min_increment_fen',
  'cap_price_fen', 'max_extend_count', 'status', 'version', 'current_price_fen',
  'leading_user_id', 'leading_nickname', 'winner_user_id', 'winner_nickname',
  'final_price_fen', 'started_at_unix_ms', 'ends_at_unix_ms', 'settled_at_unix_ms',
  'cancelled_at_unix_ms', 'cancel_reason', 'bid_count', 'extend_count', 'order_id',
  'duration_ms', 'anti_snipe_window_ms', 'anti_snipe_extend_ms')
local participant_count_raw = redis.call('SCARD', participants_key)
local ranking_limit_for_read = tonumber(ARGV[10])
local ranking_rows = redis.call('ZREVRANGE', ranking_key, 0, ranking_limit_for_read - 1, 'WITHSCORES')
local ranking_user_ids = {}
for index = 1, #ranking_rows, 2 do
  table.insert(ranking_user_ids, ranking_rows[index])
end
local ranking_meta_rows = {}
if #ranking_user_ids > 0 then
  ranking_meta_rows = redis.call('HMGET', rankmeta_key, unpack(ranking_user_ids))
end
local active_lot_id = redis.call('GET', room_active_key) or ''
local lot_frozen = redis.call('EXISTS', frozen_lot_key)
local expiring_type = redis.call('TYPE', expiring_key).ok
local outbox_type = redis.call('TYPE', outbox_key).ok
local redis_time = redis.call('TIME')

-- PHASE: VALIDATE_AND_SERIALIZE
local event_id = ARGV[1]
local trace_id = ARGV[2]
local requested_order_id = ARGV[3]
local settled_status = tonumber(ARGV[4])
local failed_status = tonumber(ARGV[5])
local command_type = tonumber(ARGV[6])
local schema_version = tonumber(ARGV[7])
local terminal_ttl_seconds = tonumber(ARGV[8])
local max_fact_bytes = tonumber(ARGV[9])
local ranking_limit = tonumber(ARGV[10])
local no_bid_reason = ARGV[11]
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)

if lot_frozen == 1 then
  return reject('LOT_FROZEN')
end
if (expiring_type ~= 'none' and expiring_type ~= 'zset') or (outbox_type ~= 'none' and outbox_type ~= 'list') then
  return reject('RUNTIME_STATE_CORRUPT')
end
local lot_id = values[1] or ''
local room_id = values[2] or ''
local main_account_id = values[3] or ''
local title = values[4] or ''
local image_url = values[5] or ''
local config_version = tonumber(values[6])
local currency = values[7] or ''
local start_price_fen = tonumber(values[8])
local min_increment_fen = tonumber(values[9])
local cap_price_text = values[10] or ''
local cap_price_fen = nil
if cap_price_text ~= '' then
  cap_price_fen = tonumber(cap_price_text)
end
local max_extend_count = tonumber(values[11])
local previous_status = tonumber(values[12])
local previous_lot_version = tonumber(values[13])
local current_price_fen = tonumber(values[14])
local leading_user_id = values[15] or ''
local leading_nickname = values[16] or ''
local winner_user_id = values[17] or ''
local winner_nickname = values[18] or ''
local final_price_fen = tonumber(values[19])
local started_at_unix_ms = tonumber(values[20])
local ends_at_unix_ms = tonumber(values[21])
local settled_at_unix_ms = tonumber(values[22])
local cancelled_at_unix_ms = tonumber(values[23])
local cancel_reason = values[24] or ''
local bid_count = tonumber(values[25])
local extend_count = tonumber(values[26])
local existing_order_id = values[27] or ''
local duration_ms = tonumber(values[28])
local anti_snipe_window_ms = tonumber(values[29])
local anti_snipe_extend_ms = tonumber(values[30])
local participant_count = tonumber(participant_count_raw)

if lot_id == '' then
  return reject('RUNTIME_STATE_MISSING')
end
if not valid_uuid_v7(event_id) or room_id == '' then
  return reject('INVALID_ARGUMENT')
end
if previous_status ~= 2 and previous_status ~= 7 then
  return reject('NOT_LIVE')
end
if not exact_positive(config_version) or not valid_currency(currency) or not exact_nonnegative(start_price_fen) or not exact_positive(min_increment_fen) then
  return reject('RUNTIME_STATE_CORRUPT')
end
if cap_price_fen ~= nil and (not exact_nonnegative(cap_price_fen) or cap_price_fen < start_price_fen) then
  return reject('RUNTIME_STATE_CORRUPT')
end
if not exact_nonnegative(max_extend_count) or max_extend_count > 2147483647 or not exact_nonnegative(previous_lot_version) or previous_lot_version == max_exact_integer or not exact_nonnegative(current_price_fen) then
  return reject('RUNTIME_STATE_CORRUPT')
end
if not exact_nonnegative(final_price_fen) or not exact_nonnegative(started_at_unix_ms) or not exact_positive(ends_at_unix_ms) or not exact_nonnegative(settled_at_unix_ms) or not exact_nonnegative(cancelled_at_unix_ms) or not exact_nonnegative(bid_count) or not exact_nonnegative(participant_count) or not exact_nonnegative(extend_count) or extend_count > max_extend_count or extend_count > 2147483647 then
  return reject('RUNTIME_STATE_CORRUPT')
end
if not exact_positive(duration_ms) or not exact_nonnegative(anti_snipe_window_ms) or not exact_nonnegative(anti_snipe_extend_ms) then
  return reject('RUNTIME_STATE_CORRUPT')
end
if not exact_positive(now_ms) or settled_status ~= 3 or failed_status ~= 8 or command_type ~= 4 or schema_version ~= 1 or not exact_positive(terminal_ttl_seconds) or not exact_positive(max_fact_bytes) or not exact_positive(ranking_limit) or ranking_limit > 100 or no_bid_reason == '' then
  return reject('INVALID_ARGUMENT')
end
if ends_at_unix_ms > now_ms then
  return reject('NOT_EXPIRED', ends_at_unix_ms)
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

local next_status = failed_status
local next_winner_user_id = winner_user_id
local next_winner_nickname = winner_nickname
local next_final_price_fen = final_price_fen
local next_settled_at_unix_ms = settled_at_unix_ms
local next_cancelled_at_unix_ms = now_ms
local next_cancel_reason = no_bid_reason
local next_order_id = existing_order_id
local order_draft = nil
if leading_user_id ~= '' then
  if requested_order_id == '' or main_account_id == '' or title == '' then
    return reject('INVALID_ARGUMENT')
  end
  next_status = settled_status
  next_winner_user_id = leading_user_id
  next_winner_nickname = leading_nickname
  next_final_price_fen = current_price_fen
  next_settled_at_unix_ms = now_ms
  next_cancelled_at_unix_ms = cancelled_at_unix_ms
  next_cancel_reason = cancel_reason
  next_order_id = requested_order_id
  order_draft = {
    order_id = requested_order_id,
    lot_id = lot_id,
    room_id = room_id,
    main_account_id = main_account_id,
    buyer_user_id = leading_user_id,
    buyer_nickname = leading_nickname,
    title = title,
    image_url = image_url,
    total_amount_fen = current_price_fen,
    currency = currency,
    created_at_unix_ms = now_ms
  }
end

local lot_version = previous_lot_version + 1
local state_after = {
  lot_id = lot_id,
  room_id = room_id,
  status = next_status,
  currency = currency,
  start_price_fen = start_price_fen,
  min_increment_fen = min_increment_fen,
  current_price_fen = current_price_fen,
  leading_user_id = leading_user_id,
  leading_nickname = leading_nickname,
  winner_user_id = next_winner_user_id,
  winner_nickname = next_winner_nickname,
  final_price_fen = next_final_price_fen,
  started_at_unix_ms = started_at_unix_ms,
  ends_at_unix_ms = ends_at_unix_ms,
  settled_at_unix_ms = next_settled_at_unix_ms,
  cancelled_at_unix_ms = next_cancelled_at_unix_ms,
  cancel_reason = next_cancel_reason,
  bid_count = bid_count,
  participant_count = participant_count,
  extend_count = extend_count,
  max_extend_count = max_extend_count,
  order_id = next_order_id,
  duration_ms = duration_ms,
  anti_snipe_window_ms = anti_snipe_window_ms,
  anti_snipe_extend_ms = anti_snipe_extend_ms
}
if cap_price_fen ~= nil then
  state_after.cap_price_fen = cap_price_fen
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
  config_version = config_version,
  command = command_type,
  state_after = state_after,
  schema_version = schema_version
}
if order_draft ~= nil then
  runtime_fact.order_draft = order_draft
end
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
  settled = next_status == settled_status,
  fact_json = fact_payload
})
local release_room = active_lot_id == lot_id

-- PHASE: WRITE
redis.call('HSET', state_key,
  'status', next_status,
  'version', lot_version,
  'last_event_id', event_id,
  'state_after_json', state_after_payload,
  'winner_user_id', next_winner_user_id,
  'winner_nickname', next_winner_nickname,
  'final_price_fen', next_final_price_fen,
  'settled_at_unix_ms', next_settled_at_unix_ms,
  'cancelled_at_unix_ms', next_cancelled_at_unix_ms,
  'cancel_reason', next_cancel_reason,
  'order_id', next_order_id)
redis.call('ZREM', expiring_key, lot_id)
if release_room then
  redis.call('DEL', room_active_key)
  redis.call('SETEX', room_display_key, terminal_ttl_seconds, lot_id)
end
redis.call('EXPIRE', state_key, terminal_ttl_seconds)
redis.call('EXPIRE', ranking_key, terminal_ttl_seconds)
redis.call('EXPIRE', rankmeta_key, terminal_ttl_seconds)
redis.call('EXPIRE', participants_key, terminal_ttl_seconds)
redis.call('EXPIRE', recent_key, terminal_ttl_seconds)
redis.call('EXPIRE', idempotency_key, terminal_ttl_seconds)
redis.call('LPUSH', outbox_key, outbox_item)
return response_payload
