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

-- Per-dimension disable: when capacity <= 0 (operator seta RPS=0 para
-- desativar burst control mantendo RPM, ou vice-versa) o bucket daquela
-- janela é tratado como "always allowed". Isso evita (a) divisão por
-- zero na computação de reset_ms quando refill_per_ms é 0 e (b) inversão
-- da semântica documentada em bucket.go ("0 capacity disables the
-- corresponding window"). HI-01 fix (Phase 4 review).
local rps_disabled = rps_cap <= 0
local rpm_disabled = rpm_cap <= 0

local rps_filled
if rps_disabled then
    rps_filled = req -- passará o check `rps_filled < req`
else
    local rps_tokens = tonumber(redis.call("get", KEYS[1])) or rps_cap
    local rps_ts     = tonumber(redis.call("get", KEYS[2])) or now
    rps_filled = math.min(rps_cap, rps_tokens + math.max(0, now - rps_ts) * rps_rate)
end

local rpm_filled
if rpm_disabled then
    rpm_filled = req
else
    local rpm_tokens = tonumber(redis.call("get", KEYS[3])) or rpm_cap
    local rpm_ts     = tonumber(redis.call("get", KEYS[4])) or now
    rpm_filled = math.min(rpm_cap, rpm_tokens + math.max(0, now - rpm_ts) * rpm_rate)
end

if not rps_disabled and rps_filled < req then
    local reset_rps_ms = math.ceil((req - rps_filled) / rps_rate)
    return {0, math.floor(rps_filled), reset_rps_ms, math.floor(rpm_filled), 0, "rps"}
end
if not rpm_disabled and rpm_filled < req then
    local reset_rpm_ms = math.ceil((req - rpm_filled) / rpm_rate)
    return {0, math.floor(rps_filled), 0, math.floor(rpm_filled), reset_rpm_ms, "rpm"}
end

local new_rps = rps_filled - req
local new_rpm = rpm_filled - req

-- Persist counters only for enabled windows. Disabled windows skip the
-- SETEX round-trip entirely — no spurious keys + no TTL math on a
-- zero-rate that would divide by zero.
if not rps_disabled then
    local rps_ttl = math.max(60, math.min(7200, math.floor((rps_cap / rps_rate) / 1000 * 2)))
    redis.call("setex", KEYS[1], rps_ttl, new_rps)
    redis.call("setex", KEYS[2], rps_ttl, now)
end
if not rpm_disabled then
    local rpm_ttl = math.max(60, math.min(7200, math.floor((rpm_cap / rpm_rate) / 1000 * 2)))
    redis.call("setex", KEYS[3], rpm_ttl, new_rpm)
    redis.call("setex", KEYS[4], rpm_ttl, now)
end

return {1, math.floor(new_rps), 0, math.floor(new_rpm), 0, ""}
