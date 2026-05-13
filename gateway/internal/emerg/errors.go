// Package emerg implements the Phase 6 emergency-pod reconciler (FSM,
// leader-elected, Vast.ai-backed). The reconciler observes the local-llm
// breaker via gw:upstreams:events and, when local-llm stays OPEN past
// PROVISION_TRIGGER_FAILED_OVER_SECONDS, transitions the FSM into
// EMERGENCY_PROVISIONING and bids on a Vast.ai offer to bring up a
// substitute pod with the same ifix-ai-pod image as the primary. See
// .planning/phases/06-auto-provisioning-emergency-pod-vast-ai/06-CONTEXT.md.
//
// Authoritative state lives in-process; Redis (gw:emerg:state Hash +
// gw:emerg:events Pub/Sub) is a mirror used for cross-replica visibility.
// Leader-election uses go-redsync/redsync/v4 against gw:emerg:lock.
package emerg

import "errors"

var (
	// ErrOfferRaceLost is returned by the lifecycle when create_instance
	// failed with 404/409 (offer already accepted by another buyer) on
	// every retry attempt (D-A3, default 3 attempts with 2s/4s/8s
	// exponential backoff). Surfaces in audit as
	// shutdown_reason='offer_race_lost' + Sentry CaptureMessage.
	ErrOfferRaceLost = errors.New("emerg: bid race lost after 3 attempts")

	// ErrHealthTimeout is returned by the lifecycle when the freshly
	// created instance never returned a healthy {llm:true,stt:true,embed:true}
	// response from /health within PROVISION_COLDSTART_BUDGET_SECONDS
	// (D-A4, default 600s). The lifecycle issues vast.destroy(instance_id)
	// before returning to ensure no leaked pod survives the timeout.
	// Surfaces in audit as shutdown_reason='health_timeout'.
	ErrHealthTimeout = errors.New("emerg: pod /health did not pass within budget")

	// ErrInstanceTerminal is returned when vast.GetInstance reports a
	// terminal state (offline, destroyed, scheduling_failed) for the
	// current lifecycle's instance_id during the polling loop. Indicates
	// Vast.ai destroyed the instance without our consent (host failure,
	// underbid by spot, etc.). Surfaces as shutdown_reason='instance_terminal_state'.
	ErrInstanceTerminal = errors.New("emerg: vast instance entered terminal state")

	// ErrNoOffersBelowCap is returned by the offer-selection step when
	// search_offers returns zero results passing the strict filter
	// (gpu_name=RTX 4090, reliability>=0.99, dph_total<=cap, inet_down>=500,
	// cuda_max_good>=12.4, host_id != PRIMARY_HOST_ID). Lifecycle aborts
	// without creating any instance.
	ErrNoOffersBelowCap = errors.New("emerg: no offers below VAST_PRICE_CAP_DPH")

	// ErrLeaderLost is returned by the leader-election goroutine when
	// redsync.Mutex.Extend() fails (Redis blip exceeded the 30s lock TTL,
	// or another replica grabbed the lock). The reconciler immediately
	// flips is_leader=false and stops advancing the FSM until it can
	// re-acquire the lock at the next tick.
	ErrLeaderLost = errors.New("emerg: leadership lost mid-tick")

	// ErrLifecycleSingleton is returned by the reconciler when, during
	// the EMERGENCY_PROVISIONING transition, a SELECT COUNT(*) FROM
	// emergency_lifecycles WHERE ended_at IS NULL returns >=1. Defense-
	// in-depth on top of leader lock + DB partial unique index (D-B5);
	// indicates a leader recovery race or operator-injected duplicate.
	// Operator must investigate via gatewayctl emerg lifecycles.
	ErrLifecycleSingleton = errors.New("emerg: live lifecycle already exists (D-B5 violation)")
)
