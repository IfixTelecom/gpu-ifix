---
status: partial
phase: 05-load-shedding-saturation-aware-routing
source: [05-VERIFICATION.md]
started: 2026-05-11T23:18:53Z
updated: 2026-05-11T23:18:53Z
---

## Current Test

[awaiting human testing — deploy ai-gateway-dev stack + active Vast.ai 4090 pod]

## Tests

### 1. SC-1 (LIVE UAT) — Burst overflow → tier-1 under real 4090 load
expected: Vegeta 50 RPS x 30s vs api-dev.converse-ai.app w/ tenant converseai drives FSM=ON; excess hits OpenRouter tier-1; FSM=OFF in ≤90s after load stops. Verified via Grafana `gateway_shed_state{upstream="local-llm"}` + `gateway_shed_decisions_total{reason}`.
result: [pending]

### 2. SC-2 (LIVE UAT) — Hysteresis convergence under 60s oscillating load
expected: Under sustained P95 spike or VRAM > 21 GB, shedding ativa ≤30s; load oscilante por 60s NÃO causa flap (≤4 FSM transitions observed via `gw:shed:events` Pub/Sub). Production thresholds arm=30s, recover=60s.
result: [pending]

### 3. SC-3 (LIVE UAT) — Hot-reload via gatewayctl thresholds set
expected: Operator runs `docker exec ifix-ai-gateway /gatewayctl thresholds set local-llm --shed-inflight-max 1000` durante burst ativo; Grafana mostra `gateway_shed_state` transição para 0 em ≤2s. NOTIFY upstreams_changed pipeline confirmado.
result: [pending]

### 4. SC-4 (LIVE UAT) — Anti-starvation priority_tier S vs B under shed
expected: Durante burst tenant A (priority_tier='B' cap=1), tenant B (priority_tier='S' cap≥4) mantém success ≥0.95 + P99 ≤2s. Per-tenant hard caps + priority_tier observados em audit_log + Grafana per-tenant inflight gauge.
result: [pending]

### 5. DCGM_EXPORTER_URL post-deploy wiring + VRAM signal smoke
expected: Operator define `DCGM_EXPORTER_URL=http://<vast-ai-pod>:9400/metrics` no Portainer env, restart container, roda `curl -sS --max-time 3 http://<pod>:9400/metrics | grep ^DCGM_FI_DEV_FB_USED` de dentro do gateway container; `gateway_vram_used_mib` Grafana gauge populates em ≤1 scrape interval (5s).
result: [pending]

### 6. Testcontainers integration suite (11 tests) — Docker daemon required
expected: `cd gateway && go test -tags=integration ./internal/integration_test/... -run 'TestSC|TestSensitive|TestTier1|TestPeakOff|TestShedForce|TestDCGM|TestMirror|TestBootRehydr'` passa em ~90s. `go test -tags='integration integration_slow' ... -run TestSC2` passa em ~125s. Rodar em host Docker-capable (vps-ifix-vm OU laptop com Docker Desktop).
result: [pending]

## Summary

total: 6
passed: 0
issues: 0
pending: 6
skipped: 0
blocked: 0

## Gaps
