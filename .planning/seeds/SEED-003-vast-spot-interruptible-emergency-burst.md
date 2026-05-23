---
id: SEED-003
status: dormant
planted: 2026-05-22
planted_during: Phase 06.6 (tech debt #4 closure session)
trigger_when: Phase 07 (Observability) lands AND first emergency pod activation observed in production traffic
scope: small
---

# SEED-003: Vast.ai spot/interruptible support for emergency burst pod

## Why This Matters

Vast offer survey via `DefaultSearchFilter` for 2×RTX 3090 48GB shows spot offers **50-92% cheaper** than on-demand for the SAME machine. Concrete observations (2026-05-22):

| min_bid | dph_total | savings | machine | geo |
|---|---|---|---|---|
| $0.107/h | $1.47/h | **92%** | 16146 | Quebec CA |
| $0.213/h | $0.43/h | 50% | 52359 | Quebec CA |
| $0.267/h | $0.43/h | 37% | 54516 | Quebec CA |
| $0.308/h | $1.07/h | 71% | 12131 | Spain ES |

Today the gateway provisions emergency pods on-demand only — leaving 50-90% of the budget on the table for the role that **least cares about eviction**. With `MONTHLY_EMERGENCY_BUDGET_BRL=200` and on-demand 4090 ~$0.30/h, the cap buys ~250h/month. With spot at ~$0.15/h, the same cap buys ~500h. Emergency is exactly the role where this scales matter — burst activations during primary outages are short and the customer is already on a degraded-mode SLA.

**Why emergency, not primary:**
- Emergency pod = degraded-mode fallback when primary breaker opens. Customer already on "best effort" SLA.
- Eviction (outbid) = same terminal-class failure as CDI / host-offline / GPU-error that the emerg FSM already handles via reconciler → close lifecycle → re-provision.
- Primary stays on-demand because eviction during the 14h customer-facing window = unacceptable.

## When to Surface

**Trigger:** Phase 07 (Observability) lands AND first emergency pod activation observed in production traffic.

Rationale: spot savings only matter once emerg pods are actually burning budget. Phase 07 adds the metric that quantifies eviction rate (`gateway_emerg_evictions_total`) — without that, "is spot worth the risk?" is unfalsifiable. Before prod emerg activations, no savings to capture.

## Scope Estimate

**Small** — quick-task (~2-3h dev + 1 live UAT $0.30) OR a single new phase 06.9 depending on test depth desired.

### Implementation sketch

1. New env `EMERGENCY_USE_INTERRUPTIBLE=true` + `EMERGENCY_VAST_BID_DPH=0.30` (cap on max bid). Default `false` — opt-in until UAT proves stability.
2. `gateway/internal/emerg/vast/types.go` `DefaultSearchFilter` accepts an `interruptible bool` param: adds `min_bid:{lte:cap}` filter + changes `order` to `[["min_bid","asc"]]`.
3. `vast.CreateInstance` in `gateway/internal/emerg/vast/client.go` accepts an optional `bid_price float64`. When `>0`, includes `"price": <bid>` in PUT `/asks/{id}/` body → Vast creates as interruptible.
4. Reconciler `gateway/internal/emerg/reconciler.go` reads new env, plumbs through to both search + create.
5. Eviction detection: Vast `cur_state=stopped` + `intended_status=stopped` is distinct from `actual_status=offline`. Either way the reconciler treats as terminal; lifecycle `shutdown_reason` gets a new enum value `evicted`.
6. Telemetry: new metric `gateway_emerg_evictions_total{role}` labelled by emerg role; informs whether spot is worth keeping.
7. Tests: `vast/types_test.go` (filter composition) + `reconciler_test.go` (create payload includes price; eviction detection treats stopped+stopped as terminal).

### Open questions for discuss/spec-phase

- **Default-on vs opt-in?** Lean opt-in: env flag false by default; flip on after one live UAT proves stability under real eviction.
- **Bid strategy:** fixed cap ($0.30/h) vs dynamic (track recent fill prices)? Start fixed.
- **Cost guard:** with spot, `EMERGENCY_MONTHLY_BUDGET_BRL=200` buys ~833h instead of ~250h. Adjust cap downward (so cost guard still trips meaningfully) or leave loose (cheaper to fail open).
- **In-flight request handling:** breaker takes N consecutive failures to open; for spot eviction we want fast cutover. Tune `ConsecutiveFailures` on the emergency upstream row separately from primary (already supported in the per-row `circuit_config` JSONB on `ai_gateway.upstreams`).

## Breadcrumbs

- `gateway/internal/emerg/vast/types.go` — `DefaultSearchFilter` + `WithMachineAllowlist` (filter base; needs `min_bid` filter + sort branch when `interruptible=true`).
- `gateway/internal/emerg/vast/client.go` — `CreateInstance` (needs optional `bid_price` field in PUT payload).
- `gateway/internal/emerg/reconciler.go` — owns the search → pick → create flow; reads env, calls filter + client.
- `gateway/internal/emerg/lifecycle.go` — terminal-status detection loop (recently extended with `status_msg` parsing for GPU-error; add `cur_state=stopped + intended_status=stopped` branch here).
- `.planning/seeds/SEED-001-emergency-pod-template-vast-vs-custom-image.md` + `SEED-002-emergency-full-pod-hot-standby-and-embed-on-pod.md` — sibling seeds in the same emerg-evolution thread; numbering convention.
- Memory: `[[vast-multi-gpu-cdi-risk]]` (failure-class similarity — eviction ≈ CDI in FSM treatment); `[[primary-gpu-shape-06.8-final]]` (standing GPU config — filter base is the same).
- STATE.md tech debt #8 (Vast driver-version filter) — sibling Vast-filter hardening; same area of code.

## Notes

Captured 2026-05-22 during td #4 closure session. The Vast survey was run with the same `DefaultSearchFilter` used by the reconciler (RTX 3090, num_gpus=2, reliability ≥0.99, cuda ≥12.8, driver ≥570000000, rentable=true) so the savings figures are real — not hypothetical inventory.

Risk model and dispatcher fallback behaviour were validated against `gateway/internal/proxy/dispatcher.go` (RES-02 deferral): in-flight request hitting a dying pod gets a single timeout/connection error; breaker opens after N consecutive failures; subsequent normal-tenant requests fall back to OpenRouter tier-1; sensitive-tenant requests 503 fail-fast (no external fallback by D-B3/D-B4). For emergency role this matches the existing UX path for on-demand pod terminal failures — no new contract violation introduced by spot.
