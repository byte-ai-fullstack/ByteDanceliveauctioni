local owner_key = KEYS[1]
local inflight_key = KEYS[2]
local expected_owner = ARGV[1]
local expected_event_id = ARGV[2]

if expected_owner == '' or expected_event_id == '' then
  return redis.error_reply('INVALID_ARGUMENT')
end
if redis.call('GET', owner_key) ~= expected_owner then
  return 'NOT_OWNER'
end
local item = redis.call('LINDEX', inflight_key, -1)
if not item then
  return 'EMPTY'
end
local newline = string.find(item, '\n', 1, true)
if not newline then
  return 'MALFORMED'
end
if string.sub(item, 1, newline - 1) ~= expected_event_id then
  return 'MISMATCH'
end
redis.call('RPOP', inflight_key)
return 'OK'
