// Package redisx (alert.go): the Redis key namespace for the Phase 7
// alerting goroutine's dedup window (OBS-04 / OBS-05).
//
// # Why a dedup key
//
// The alerter subscribes to three Pub/Sub event streams
// (gw:breaker:events, gw:shed:events, gw:emerg:events) and fans out to
// Chatwoot / ClickUp / Brevo. A single incident — say the GPU primary
// flapping — can publish the SAME logical alert many times within a few
// seconds (every breaker re-trip, every FSM re-evaluation). Without a
// dedup gate, the on-call operator's WhatsApp + a ClickUp list + an
// email inbox all get spammed.
//
// The dedup is a Redis SET NX with a short TTL: the first time an alert
// fingerprint is seen, the key is claimed and the alert is delivered;
// repeat fingerprints inside the TTL window find the key already set and
// are suppressed. A 5-minute window is the chosen default — long enough
// to collapse a flapping-incident storm into one notification, short
// enough that a genuinely recurring problem re-alerts so it is not
// silently forgotten.
//
// # What this file owns vs. does not own
//
// This file owns ONLY the key namespace: the `gw:alert:dedup:` prefix
// constant and the `AlertDedupKey(fingerprint)` builder. The actual
// SET NX call + TTL handling lives in `alert/dedup.go` (plan 07-05) so
// the alerter package controls the dedup *policy* while redisx owns the
// *key layout* — mirroring how shed.go / emerg.go keep their key
// builders here and their FSM logic in their own packages.
package redisx

// AlertDedupKeyPrefix is the Redis key namespace for alert dedup
// markers. A full key is AlertDedupKeyPrefix + <fingerprint>, e.g.
// "gw:alert:dedup:breaker:openrouter:open". The alerter (plan 07-05)
// SET NX's this key with a ~5-minute TTL; a hit means "already alerted,
// suppress". Kept as an exported const (not just a func-internal
// literal) so gatewayctl + tests can reference the namespace directly.
const AlertDedupKeyPrefix = "gw:alert:dedup:"

// AlertDedupKey returns the canonical "gw:alert:dedup:<fingerprint>"
// key for an alert fingerprint. The fingerprint is the alerter's stable
// identity for a logical alert (e.g. "breaker:<upstream>:<state>" or
// "emerg:<state>") — distinct incidents get distinct keys, repeats of
// the same incident collide on one key inside the TTL window.
//
// Wrapped in a function (vs. callers concatenating the prefix
// themselves) to mirror ShedStateKey / EmergStateKey and to leave room
// for a per-tenant or per-severity key-shard rollout without breaking
// callsites.
func AlertDedupKey(fingerprint string) string {
	return AlertDedupKeyPrefix + fingerprint
}
