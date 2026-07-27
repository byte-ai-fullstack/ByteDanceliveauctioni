-- KEYS:
-- 1 state, 2 ranking, 3 rankmeta, 4 participants, 5 recent, 6 idempotency,
-- 7 expiring zset, 8 outbox pending list, 9 room active lot, 10 frozen lot fence,
-- 11 room display lot.
-- ARGV:
-- 1 event_id, 2 trace_id, 3 bid_id, 4 user_id, 5 nickname,
-- 6 masked_nickname, 7 avatar_url, 8 amount_fen, 9 currency,
-- 10 idempotency_field, 11 idempotency_key, 12 order_id,
-- 13 live_status, 14 extended_status, 15 settled_status, 16 command_type,
-- 17 schema_version, 18 max_fact_bytes, 19 ranking_limit, 20 recent_limit,
-- 21 terminal_ttl_seconds, 22 outbox_pending_limit, 23 cancelled_status,
-- 24 failed_status.

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

local function reject(code, current_price_fen, min_increment_fen, lot_version, ends_at_unix_ms)
  local response = { ok = false, code = code, message = code }
  if current_price_fen ~= nil then
    response.current_price_fen = current_price_fen
  end
  if min_increment_fen ~= nil then
    response.min_increment_fen = min_increment_fen
  end
  if current_price_fen ~= nil and min_increment_fen ~= nil and current_price_fen <= max_exact_integer - min_increment_fen then
    response.minimum_bid_fen = current_price_fen + min_increment_fen
  end
  if lot_version ~= nil then
    response.lot_version = lot_version
  end
  if ends_at_unix_ms ~= nil then
    response.ends_at_unix_ms = ends_at_unix_ms
  end
  return cjson.encode(response)
end

-- PHASE: READ
local idempotency_field_for_read = ARGV[10]
local replay_payload = redis.call('HGET', idempotency_key, idempotency_field_for_read)
local values = redis.call('HMGET', state_key,
  'lot_id', 'room_id', 'main_account_id', 'title', 'image_url',
  'config_version', 'currency', 'start_price_fen', 'min_increment_fen',
  'cap_price_fen', 'duration_ms', 'anti_snipe_window_ms', 'anti_snipe_extend_ms',
  'max_extend_count', 'status', 'version', 'current_price_fen',
  'leading_user_id', 'leading_nickname', 'winner_user_id', 'winner_nickname',
  'final_price_fen', 'started_at_unix_ms', 'ends_at_unix_ms', 'settled_at_unix_ms',
  'cancelled_at_unix_ms', 'cancel_reason', 'bid_count', 'extend_count', 'order_id')
local participant_exists_raw = redis.call('SISMEMBER', participants_key, ARGV[4])
local participant_count_raw = redis.call('SCARD', participants_key)
local ranking_limit_for_read = tonumber(ARGV[19])
local ranking_rows = redis.call('ZREVRANGE', ranking_key, 0, ranking_limit_for_read, 'WITHSCORES')
local ranking_user_ids = {}
for index = 1, #ranking_rows, 2 do
  table.insert(ranking_user_ids, ranking_rows[index])
end
local ranking_meta_rows = {}
if #ranking_user_ids > 0 then
  ranking_meta_rows = redis.call('HMGET', rankmeta_key, unpack(ranking_user_ids))
end
local rankmeta_length_raw = redis.call('HLEN', rankmeta_key)
local recent_length_raw = redis.call('LLEN', recent_key)
local expiring_score_raw = redis.call('ZSCORE', expiring_key, values[1] or '')
local outbox_length_raw = redis.call('LLEN', outbox_key)
local active_lot_id = redis.call('GET', room_active_key) or ''
local lot_frozen = redis.call('EXISTS', frozen_lot_key)
local redis_time = redis.call('TIME')

-- PHASE: VALIDATE_AND_SERIALIZE
if replay_payload ~= false and replay_payload ~= nil then
  local replay_ok, replay = pcall(cjson.decode, replay_payload)
  if not replay_ok or type(replay) ~= 'table' or replay.ok ~= true then
    return reject('IDEMPOTENCY_STATE_CORRUPT')
  end
  replay.replayed = true
  return cjson.encode(replay)
end

local event_id = ARGV[1]
local trace_id = ARGV[2]
local bid_id = ARGV[3]
local user_id = ARGV[4]
local nickname = ARGV[5]
local masked_nickname = ARGV[6]
local avatar_url = ARGV[7]
local amount_fen = tonumber(ARGV[8])
local requested_currency = ARGV[9]
local idempotency_field = ARGV[10]
local business_idempotency_key = ARGV[11]
local requested_order_id = ARGV[12]
local live_status = tonumber(ARGV[13])
local extended_status = tonumber(ARGV[14])
local settled_status = tonumber(ARGV[15])
local command_type = tonumber(ARGV[16])
local schema_version = tonumber(ARGV[17])
local max_fact_bytes = tonumber(ARGV[18])
local ranking_limit = tonumber(ARGV[19])
local recent_limit = tonumber(ARGV[20])
local terminal_ttl_seconds = tonumber(ARGV[21])
local outbox_pending_limit = tonumber(ARGV[22])
local cancelled_status = tonumber(ARGV[23])
local failed_status = tonumber(ARGV[24])
local now_ms = tonumber(redis_time[1]) * 1000 + math.floor(tonumber(redis_time[2]) / 1000)

if lot_frozen == 1 then
  return reject('LOT_FROZEN')
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
local duration_ms = tonumber(values[11])
local anti_snipe_window_ms = tonumber(values[12])
local anti_snipe_extend_ms = tonumber(values[13])
local max_extend_count = tonumber(values[14])
local previous_status = tonumber(values[15])
local previous_lot_version = tonumber(values[16])
local current_price_fen = tonumber(values[17])
local previous_leading_user_id = values[18] or ''
local previous_leading_nickname = values[19] or ''
local winner_user_id = values[20] or ''
local winner_nickname = values[21] or ''
local final_price_fen = tonumber(values[22])
local started_at_unix_ms = tonumber(values[23])
local ends_at_unix_ms = tonumber(values[24])
local settled_at_unix_ms = tonumber(values[25])
local cancelled_at_unix_ms = tonumber(values[26])
local cancel_reason = values[27] or ''
local previous_bid_count = tonumber(values[28])
local previous_extend_count = tonumber(values[29])
local existing_order_id = values[30] or ''
local participant_exists = tonumber(participant_exists_raw)
local previous_participant_count = tonumber(participant_count_raw)
local rankmeta_length = tonumber(rankmeta_length_raw)
local recent_length = tonumber(recent_length_raw)
local outbox_length = tonumber(outbox_length_raw)

if lot_id == '' then
  return reject('RUNTIME_STATE_MISSING')
end
if not valid_uuid_v7(event_id) or room_id == '' or bid_id == '' or user_id == '' or nickname == '' or masked_nickname == '' or idempotency_field == '' or business_idempotency_key == '' then
  return reject('INVALID_ARGUMENT')
end
if not exact_positive(config_version) or not valid_currency(currency) or not exact_nonnegative(start_price_fen) or not exact_positive(min_increment_fen) or start_price_fen > max_exact_integer - min_increment_fen then
  return reject('RUNTIME_STATE_CORRUPT')
end
if cap_price_fen ~= nil and (not exact_nonnegative(cap_price_fen) or cap_price_fen < start_price_fen) then
  return reject('RUNTIME_STATE_CORRUPT')
end
if not exact_positive(duration_ms) or not exact_nonnegative(anti_snipe_window_ms) or not exact_nonnegative(anti_snipe_extend_ms) or not exact_nonnegative(max_extend_count) or max_extend_count > 2147483647 then
  return reject('RUNTIME_STATE_CORRUPT')
end
if not exact_nonnegative(previous_lot_version) or previous_lot_version == max_exact_integer or not exact_nonnegative(current_price_fen) or not exact_nonnegative(final_price_fen) or not exact_nonnegative(started_at_unix_ms) or not exact_positive(ends_at_unix_ms) or not exact_nonnegative(settled_at_unix_ms) or not exact_nonnegative(cancelled_at_unix_ms) then
  return reject('RUNTIME_STATE_CORRUPT')
end
if not exact_nonnegative(previous_bid_count) or previous_bid_count == max_exact_integer or not exact_nonnegative(previous_extend_count) or previous_extend_count > max_extend_count or previous_extend_count > 2147483647 or not exact_nonnegative(previous_participant_count) or not exact_nonnegative(participant_exists) or not exact_nonnegative(rankmeta_length) or not exact_nonnegative(recent_length) or not exact_nonnegative(outbox_length) then
  return reject('RUNTIME_STATE_CORRUPT')
end
if not exact_positive(now_ms) or not exact_nonnegative(amount_fen) or live_status ~= 2 or extended_status ~= 7 or settled_status ~= 3 or cancelled_status ~= 4 or failed_status ~= 8 or command_type ~= 2 or schema_version ~= 1 or not exact_positive(max_fact_bytes) or not exact_positive(ranking_limit) or ranking_limit > 100 or not exact_positive(recent_limit) or recent_limit > 100 or not exact_positive(terminal_ttl_seconds) or not exact_nonnegative(outbox_pending_limit) then
  return reject('INVALID_ARGUMENT')
end
if previous_status == cancelled_status then
  return reject('LOT_CANCELLED', current_price_fen, min_increment_fen, previous_lot_version, ends_at_unix_ms)
end
if previous_status == settled_status or previous_status == failed_status then
  return reject('BID_ENDED', current_price_fen, min_increment_fen, previous_lot_version, ends_at_unix_ms)
end
if previous_status ~= live_status and previous_status ~= extended_status then
  return reject('BID_NOT_LIVE', current_price_fen, min_increment_fen, previous_lot_version, ends_at_unix_ms)
end
if active_lot_id ~= lot_id then
  return reject('RUNTIME_NOT_ACTIVE')
end
if outbox_pending_limit > 0 and outbox_length >= outbox_pending_limit then
  return reject('PROJECTION_PENDING')
end
if now_ms >= ends_at_unix_ms then
  return reject('BID_ENDED', current_price_fen, min_increment_fen, previous_lot_version, ends_at_unix_ms)
end
if requested_currency ~= currency then
  return reject('BID_CURRENCY_MISMATCH', current_price_fen, min_increment_fen, previous_lot_version, ends_at_unix_ms)
end
if previous_leading_user_id == user_id then
  return reject('BID_ALREADY_LEADING', current_price_fen, min_increment_fen, previous_lot_version, ends_at_unix_ms)
end
if current_price_fen > max_exact_integer - min_increment_fen then
  return reject('RUNTIME_STATE_CORRUPT')
end
local minimum_bid_fen = current_price_fen + min_increment_fen
if amount_fen < minimum_bid_fen then
  return reject('BID_TOO_LOW', current_price_fen, min_increment_fen, previous_lot_version, ends_at_unix_ms)
end

local ranking_top = {{
  rank = 1,
  user_id = user_id,
  masked_nickname = masked_nickname,
  avatar_url = avatar_url,
  amount_fen = amount_fen,
  bid_at_unix_ms = now_ms
}}
for index = 1, #ranking_user_ids do
  local ranking_user_id = ranking_user_ids[index]
  if ranking_user_id ~= user_id and #ranking_top < ranking_limit then
    local raw_meta = ranking_meta_rows[index]
    if raw_meta == false or raw_meta == nil then
      return reject('RUNTIME_STATE_CORRUPT')
    end
    local decoded_ok, meta = pcall(cjson.decode, raw_meta)
    local ranking_amount_fen = tonumber(ranking_rows[index * 2])
    if not decoded_ok or type(meta) ~= 'table' or not exact_nonnegative(ranking_amount_fen) or ranking_amount_fen > current_price_fen or not exact_positive(tonumber(meta.bid_at_unix_ms)) then
      return reject('RUNTIME_STATE_CORRUPT')
    end
    table.insert(ranking_top, {
      rank = #ranking_top + 1,
      user_id = ranking_user_id,
      masked_nickname = meta.masked_nickname or '',
      avatar_url = meta.avatar_url or '',
      amount_fen = ranking_amount_fen,
      bid_at_unix_ms = tonumber(meta.bid_at_unix_ms)
    })
  end
end

local lot_version = previous_lot_version + 1
local bid_count = previous_bid_count + 1
local participant_count = previous_participant_count
if participant_exists == 0 then
  if previous_participant_count == max_exact_integer then
    return reject('RUNTIME_STATE_CORRUPT')
  end
  participant_count = participant_count + 1
end
local next_status = previous_status
local next_ends_at_unix_ms = ends_at_unix_ms
local next_extend_count = previous_extend_count
local next_winner_user_id = winner_user_id
local next_winner_nickname = winner_nickname
local next_final_price_fen = final_price_fen
local next_settled_at_unix_ms = settled_at_unix_ms
local next_order_id = existing_order_id
local order_draft = nil
local terminal = false
if cap_price_fen ~= nil and amount_fen >= cap_price_fen then
  if requested_order_id == '' or main_account_id == '' or title == '' then
    return reject('INVALID_ARGUMENT')
  end
  terminal = true
  next_status = settled_status
  next_winner_user_id = user_id
  next_winner_nickname = nickname
  next_final_price_fen = amount_fen
  next_settled_at_unix_ms = now_ms
  next_order_id = requested_order_id
  order_draft = {
    order_id = requested_order_id,
    lot_id = lot_id,
    room_id = room_id,
    main_account_id = main_account_id,
    buyer_user_id = user_id,
    buyer_nickname = nickname,
    title = title,
    image_url = image_url,
    total_amount_fen = amount_fen,
    currency = currency,
    created_at_unix_ms = now_ms
  }
else
  local remaining_ms = ends_at_unix_ms - now_ms
  if remaining_ms > 0 and remaining_ms <= anti_snipe_window_ms and previous_extend_count < max_extend_count then
    if ends_at_unix_ms > max_exact_integer - anti_snipe_extend_ms then
      return reject('RUNTIME_STATE_CORRUPT')
    end
    next_ends_at_unix_ms = ends_at_unix_ms + anti_snipe_extend_ms
    next_extend_count = previous_extend_count + 1
    next_status = extended_status
  end
end

local state_after = {
  lot_id = lot_id,
  room_id = room_id,
  status = next_status,
  currency = currency,
  start_price_fen = start_price_fen,
  min_increment_fen = min_increment_fen,
  current_price_fen = amount_fen,
  leading_user_id = user_id,
  leading_nickname = nickname,
  winner_user_id = next_winner_user_id,
  winner_nickname = next_winner_nickname,
  final_price_fen = next_final_price_fen,
  started_at_unix_ms = started_at_unix_ms,
  ends_at_unix_ms = next_ends_at_unix_ms,
  settled_at_unix_ms = next_settled_at_unix_ms,
  cancelled_at_unix_ms = cancelled_at_unix_ms,
  cancel_reason = cancel_reason,
  bid_count = bid_count,
  participant_count = participant_count,
  extend_count = next_extend_count,
  max_extend_count = max_extend_count,
  order_id = next_order_id,
  top_ranking = ranking_top,
  duration_ms = duration_ms,
  anti_snipe_window_ms = anti_snipe_window_ms,
  anti_snipe_extend_ms = anti_snipe_extend_ms
}
if cap_price_fen ~= nil then
  state_after.cap_price_fen = cap_price_fen
end
local accepted_bid = {
  bid_id = bid_id,
  user_id = user_id,
  nickname = nickname,
  avatar_url = avatar_url,
  amount_fen = amount_fen,
  accepted_at_unix_ms = now_ms
}
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
  accepted_bid = accepted_bid,
  idempotency_key = business_idempotency_key,
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
local rankmeta_payload = cjson.encode({
  user_id = user_id,
  nickname = nickname,
  masked_nickname = masked_nickname,
  avatar_url = avatar_url,
  amount = amount_fen,
  amount_fen = amount_fen,
  currency = currency,
  bid_at_unix_ms = now_ms,
  bid_id = bid_id
})
local recent_payload = cjson.encode({
  id = bid_id,
  lot_id = lot_id,
  user_id = user_id,
  nickname = nickname,
  avatar_url = avatar_url,
  amount = amount_fen,
  currency = currency,
  created_at_unix_ms = now_ms
})
local response_payload = cjson.encode({
  ok = true,
  replayed = false,
  event_id = event_id,
  lot_version = lot_version,
  occurred_at_unix_ms = now_ms,
  order_id = next_order_id,
  fact_json = fact_payload
})
local release_room = terminal and active_lot_id == lot_id

-- PHASE: WRITE
redis.call('HSET', state_key,
  'status', next_status,
  'version', lot_version,
  'last_event_id', event_id,
  'state_after_json', state_after_payload,
  'current_price_fen', amount_fen,
  'leading_user_id', user_id,
  'leading_nickname', nickname,
  'winner_user_id', next_winner_user_id,
  'winner_nickname', next_winner_nickname,
  'final_price_fen', next_final_price_fen,
  'ends_at_unix_ms', next_ends_at_unix_ms,
  'settled_at_unix_ms', next_settled_at_unix_ms,
  'bid_count', bid_count,
  'participant_count', participant_count,
  'extend_count', next_extend_count,
  'order_id', next_order_id)
redis.call('ZADD', ranking_key, amount_fen, user_id)
redis.call('HSET', rankmeta_key, user_id, rankmeta_payload)
redis.call('SADD', participants_key, user_id)
redis.call('LPUSH', recent_key, recent_payload)
redis.call('LTRIM', recent_key, 0, recent_limit - 1)
redis.call('HSET', idempotency_key, idempotency_field, response_payload)
if terminal then
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
else
  redis.call('ZADD', expiring_key, next_ends_at_unix_ms, lot_id)
  redis.call('PERSIST', state_key)
  redis.call('PERSIST', ranking_key)
  redis.call('PERSIST', rankmeta_key)
  redis.call('PERSIST', participants_key)
  redis.call('PERSIST', recent_key)
  redis.call('PERSIST', idempotency_key)
end
redis.call('LPUSH', outbox_key, outbox_item)
return response_payload
