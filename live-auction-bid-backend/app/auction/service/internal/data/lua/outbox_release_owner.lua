local owner_key = KEYS[1]
local expected_owner = ARGV[1]

if expected_owner == '' then
  return redis.error_reply('INVALID_ARGUMENT')
end
if redis.call('GET', owner_key) ~= expected_owner then
  return 0
end
redis.call('DEL', owner_key)
return 1
