local owner_key = KEYS[1]
local expected_owner = ARGV[1]
local ttl_ms = tonumber(ARGV[2])

if expected_owner == '' or ttl_ms == nil or ttl_ms <= 0 or ttl_ms > 9007199254740991 or ttl_ms ~= math.floor(ttl_ms) then
  return redis.error_reply('INVALID_ARGUMENT')
end
if redis.call('GET', owner_key) ~= expected_owner then
  return 0
end
redis.call('PEXPIRE', owner_key, ttl_ms)
return 1
