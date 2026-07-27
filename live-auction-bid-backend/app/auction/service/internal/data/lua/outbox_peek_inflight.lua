local owner_key = KEYS[1]
local inflight_key = KEYS[2]
local expected_owner = ARGV[1]

if expected_owner == '' then
  return redis.error_reply('INVALID_ARGUMENT')
end
if redis.call('GET', owner_key) ~= expected_owner then
  return redis.error_reply('NOT_OWNER')
end
return redis.call('LINDEX', inflight_key, -1)
