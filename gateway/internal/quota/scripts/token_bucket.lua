-- Atomic RPS+RPM token bucket.
--
-- KEYS[1] = gw:rate:{tenant}:{route_class}:rps:tokens
-- KEYS[2] = gw:rate:{tenant}:{route_class}:rps:ts
-- KEYS[3] = gw:rate:{tenant}:{route_class}:rpm:tokens
-- KEYS[4] = gw:rate:{tenant}:{route_class}:rpm:ts
--
-- ARGV[1] = now_ms                    -- wall clock millis supplied by caller
-- ARGV[2] = rps_capacity              -- integer
-- ARGV[3] = rps_refill_per_ms         -- float (tokens/ms)
-- ARGV[4] = rpm_capacity              -- integer
-- ARGV[5] = rpm_refill_per_ms         -- float (tokens/ms)
-- ARGV[6] = requested                 -- integer tokens to consume (usually 1)
--
-- Returns: {allowed, remRPS, resetRPSms, remRPM, resetRPMms, failedWindow}
-- where failedWindow is one of "", "rps", "rpm".

local now      = tonumber(ARGV[1])
local rps_cap  = tonumber(ARGV[2])
local rps_rate = tonumber(ARGV[3])
local rpm_cap  = tonumber(ARGV[4])
local rpm_rate = tonumber(ARGV[5])
local req      = tonumber(ARGV[6])

local rps_tokens = tonumber(redis.call("get", KEYS[1])) or rps_cap
local rps_ts     = tonumber(redis.call("get", KEYS[2])) or now
local rps_filled = math.min(rps_cap, rps_tokens + math.max(0, now - rps_ts) * rps_rate)

local rpm_tokens = tonumber(redis.call("get", KEYS[3])) or rpm_cap
local rpm_ts     = tonumber(redis.call("get", KEYS[4])) or now
local rpm_filled = math.min(rpm_cap, rpm_tokens + math.max(0, now - rpm_ts) * rpm_rate)

if rps_filled < req then
    local reset_rps_ms = math.ceil((req - rps_filled) / rps_rate)
    return {0, math.floor(rps_filled), reset_rps_ms, math.floor(rpm_filled), 0, "rps"}
end
if rpm_filled < req then
    local reset_rpm_ms = math.ceil((req - rpm_filled) / rpm_rate)
    return {0, math.floor(rps_filled), 0, math.floor(rpm_filled), reset_rpm_ms, "rpm"}
end

local new_rps = rps_filled - req
local new_rpm = rpm_filled - req

-- TTL bounded [60s, 7200s]; roughly 2× full-refill horizon.
local rps_ttl = math.max(60, math.min(7200, math.floor((rps_cap / rps_rate) / 1000 * 2)))
local rpm_ttl = math.max(60, math.min(7200, math.floor((rpm_cap / rpm_rate) / 1000 * 2)))

redis.call("setex", KEYS[1], rps_ttl, new_rps)
redis.call("setex", KEYS[2], rps_ttl, now)
redis.call("setex", KEYS[3], rpm_ttl, new_rpm)
redis.call("setex", KEYS[4], rpm_ttl, now)

return {1, math.floor(new_rps), 0, math.floor(new_rpm), 0, ""}
