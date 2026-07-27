local owner_key = KEYS[1]
local epoch_key = KEYS[2]
local instance_id = ARGV[1]
local ttl_ms = tonumber(ARGV[2])

if instance_id == '' or ttl_ms == nil or ttl_ms <= 0 or ttl_ms > 9007199254740991 or ttl_ms ~= math.floor(ttl_ms) then
  return redis.error_reply('INVALID_ARGUMENT')
end
if redis.call('EXISTS', owner_key) == 1 then
  return {0, 0, ''}
end
local epoch = redis.call('INCR', epoch_key)
local owner = instance_id .. ':' .. tostring(epoch)
redis.call('SET', owner_key, owner, 'PX', ttl_ms)
return {1, epoch, owner}
