local owner_key = KEYS[1]
local pending_key = KEYS[2]
local inflight_key = KEYS[3]
local expected_owner = ARGV[1]

if expected_owner == '' then
  return redis.error_reply('INVALID_ARGUMENT')
end
if redis.call('GET', owner_key) ~= expected_owner then
  return redis.error_reply('NOT_OWNER')
end
if redis.call('LLEN', inflight_key) ~= 0 then
  return redis.error_reply('INFLIGHT_NOT_EMPTY')
end
return redis.call('LMOVE', pending_key, inflight_key, 'RIGHT', 'LEFT')
