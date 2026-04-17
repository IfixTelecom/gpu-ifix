# Pitfalls Research

**Domain:** AI inference gateway (Go) with failover (OpenRouter/OpenAI) and auto-provisioning of GPU pods (Vast.ai) — RTX 4090 hosting Qwen 3.5 27B Q4_K_M + Whisper large-v3 + BGE-M3, multi-tenant
**Researched:** 2026-04-17
**Confidence:** HIGH on VRAM/Vast.ai/Go/OpenRouter (Context7 + official 2026 incident reports + Github issues). MEDIUM on auto-provisioning loop patterns (analogous from K8s autoscalers, not Vast.ai specific). MEDIUM on LGPD applicability (general guidance, no Ifix-specific legal opinion).

---

## Critical Pitfalls

### Pitfall 1: VRAM budget collapses under concurrent long-context requests (Qwen 27B on 24 GB)

**What goes wrong:**
Static VRAM math assumes: Qwen 16 GB + Whisper 3 GB + BGE-M3 1 GB + overhead 2-3 GB = 22-23 GB. The "1-2 GB margin" is the margin for **the startup state**, not the runtime state. Under load:
- llama.cpp `-np N` (parallel slot count) pre-allocates KV cache per slot. At `ctx-size 16384` with GQA, each slot can consume 500 MB – 1.5 GB of VRAM. Four slots = 2-6 GB added on top of the 16 GB model weights.
- Whisper's `max_batch_size` (faster-whisper) at long audio (>5 min) spikes VRAM for decoder attention — on 24 GB A10G the safe ceiling is 64 but actual usage scales with concurrent audio length.
- The KV cache is **not** counted in `nvidia-smi` memory at t=0; it grows as tokens are generated and new requests arrive.

Result: first-hour works, then a noon spike with 3 concurrent long chats + 2 transcriptions = CUDA OOM, server process dies, **all three models go down at once** (they share the 4090).

**Why it happens:**
- Devs size VRAM against the model binary, not the working set.
- llama.cpp's `-np` pre-reservation is silent — you won't see it in nvidia-smi until the first request touches the slot.
- Whisper + Qwen + embed together means no single process controls the memory; an OOM in one kills the CUDA context for all.
- `max_model_len` in vLLM is enforced per request, but it does not cap total memory if N requests each use the max — see the active [vLLM issue on estimating max model length when KV cache is insufficient](https://github.com/vllm-project/vllm/issues/16118).

**How to avoid:**
1. Size Qwen with explicit safety: run `llama-server -np 2 --ctx-size 8192` initially (not 16384), benchmark real VRAM under 2-4 concurrent long chats, **leave 3 GB headroom**, not 1-2 GB.
2. Isolate model processes — each on separate CUDA context / MIG slice if possible, so one OOM doesn't kill all three. At minimum, run each in its own container with `--memory` and `--gpus` limits.
3. Enforce per-request token limits at the gateway **before** sending to llama.cpp. Reject requests over 8k tokens with a 400, don't let them reach the model.
4. Limit Whisper concurrency at the gateway (e.g., max 2 in-flight Whisper requests); queue excess.
5. Monitor `nvidia-smi` memory used vs free every 10s. Trip load-shedding (shed to OpenRouter) at 90% VRAM, not 99%.
6. On Qwen, avoid `ctx-size 16384` as default; bump it only for a specific API key that needs it, backed by a separate slot pool.

**Warning signs:**
- `nvidia-smi` shows free VRAM dropping below 2 GB during normal traffic.
- llama.cpp logs `ggml_cuda_host_malloc: failed` or `CUDA error: out of memory`.
- First-token latency (TTFT) doubles compared to warm state (KV cache thrashing).
- Intermittent 500s on concurrent requests while single-request tests pass.
- Whisper latency spikes while LLM latency is flat, or vice versa — resource contention visible.

**Phase to address:**
Phase 1 (GPU stack bring-up): size `-np` and `ctx-size` empirically, measure VRAM under load, document headroom. Phase 3 (gateway v1): enforce pre-model token caps and Whisper concurrency limits.

---

### Pitfall 2: Saturation detection via GPU util % triggers false failovers (util at 100% is normal)

**What goes wrong:**
Reading `nvidia-smi --query-gpu=utilization.gpu` and triggering load-shed to OpenRouter at `>95%` seems reasonable. It is wrong. **During any LLM inference, a single healthy request will drive GPU util to 100%** — that's the GPU doing its job, not saturation. The gateway starts bleeding to OpenRouter on every call. Cost spikes. Local GPU sits underused because the gateway "thinks" it's full.

Conversely, a queue forming behind a slow single request can sit at 100% with one slot active and 20 requests waiting — **util doesn't reveal the queue.**

Also: [nvidia-smi samples at 1/6 second intervals](https://forums.developer.nvidia.com/t/is-there-sample-period-change-available-for-nvidia-smi/203656). Sampling can miss short kernels and mis-report brief idle windows as "available capacity." Saturation detection flaps.

**Why it happens:**
- "100% = saturated" is a CPU-era mental model. For GPUs, util % means "≥1 kernel running," not "out of compute."
- Gateway authors rarely wire in queue depth from the inference server (llama.cpp and Whisper servers don't expose nice metrics by default).
- nvidia-smi is the easy API; proper DCGM or NVML telemetry isn't.

**How to avoid:**
1. Do **not** use util % alone as saturation signal. Combine:
   - **VRAM free < 10%** (strong saturation signal)
   - **In-flight request count at gateway >= slot count configured for llama.cpp** (queue forming)
   - **p95 TTFT > threshold over a 30s window** (latency saturation)
2. Require all three (VRAM OR queue-forming OR p95-latency-over-threshold) with a **2-out-of-3** or time-sustained gate to avoid flapping from a single spiky metric.
3. Track queue depth in the gateway itself (semaphore counting in-flight-per-model). This is the ground-truth saturation signal — much more reliable than GPU telemetry.
4. Apply **hysteresis**: shed-to-external turns on when condition holds for ≥30s, turns off only after condition clears for ≥60s — [flapping mitigation via hysteresis](https://www.systemoverflow.com/learn/resilience-patterns/circuit-breaker/circuit-breaker-failure-modes-flapping-stampedes-and-retry-amplification).
5. For per-model attribution (Qwen vs Whisper), track in-flight per model in the gateway. nvidia-smi gives you aggregate, not per-process memory; `--query-compute-apps=pid,used_memory` gives per-process.

**Warning signs:**
- Grafana shows load-shedding "active" percentage > 5% when the actual concurrent user count is low.
- OpenRouter bill climbs while the Vast.ai GPU shows normal `power.draw` (i.e., it was under-used while we were paying for fallback).
- Oscillation in logs: "shedding on" / "shedding off" within < 2 min repeatedly.

**Phase to address:**
Phase 4 (load-shedding and saturation detection). Explicitly include hysteresis and composite signal design as acceptance criteria.

---

### Pitfall 3: Streaming response in flight when primary dies → replay produces duplicated output

**What goes wrong:**
Client sends `/v1/chat/completions` with `stream: true`. Gateway forwards to local Qwen. Qwen emits 400 tokens, then dies (Vast.ai host reboot, OOM, network blip). Gateway's failover logic kicks in, retries against OpenRouter. OpenRouter generates a **full new response from the original prompt**, not continuing where Qwen left off. Client sees:
- 400 tokens of partial Qwen output
- Followed by a new, independent OpenRouter response from token 1

The UX is a scrambled message. Worse: if the gateway also strips Qwen's partial output and **only** sends OpenRouter's, the app has already painted Qwen's tokens in the UI (they were streamed). Two choices, both bad.

This is magnified for **tool calls**. Qwen streamed `{"tool_call":{"name":"search"` before dying. OpenRouter's replay emits a full tool call from scratch. The client (or agent loop) may execute **both** tool calls, charging the user twice and creating inconsistent state.

**Why it happens:**
- Streaming mid-response failure is the hardest LLM-gateway problem — see [LLM Gateway's failover writeup](https://llmgateway.io/blog/how-we-handle-llm-provider-failover).
- There's no protocol for "resume generation at token N" across providers.
- Gateways often retry naively: same request, different provider, client never knows.

**How to avoid:**
1. **Do not retry streams that have already started emitting tokens.** Three options, pick one explicitly:
   - **A (Fail-fast)**: If primary dies mid-stream, close the stream with an error SSE event. Client re-initiates. Tell clients this in the contract.
   - **B (No retry for streams)**: Retry is only enabled for non-streaming requests. Document it.
   - **C (Replay with marker)**: Send an SSE event like `event: provider_switch, data: {"truncated_at": 400}` then stream OpenRouter from scratch. Client must be marker-aware. **This requires a client SDK you control** — Ifix does, since all apps are internal.
2. For non-streaming requests (`stream: false`), retry is safe — client hasn't seen the response body yet.
3. For **tool calls**: track tool-call IDs at the gateway. If a failover happens after a tool call was already emitted, abort instead of retrying, and surface a 502. The agent retries at its layer (it has idempotency context the gateway does not).
4. Idempotency keys: accept `Idempotency-Key` header from clients; gateway caches response metadata (not content) for 5 min. Second retry with same key returns cached outcome instead of re-running.

**Warning signs:**
- Support reports of "weird chatbot responses that start over partway through."
- Tool-use duplicate side effects (two emails sent, double CRM update).
- OpenRouter billing shows tokens for requests that also billed the local Qwen.

**Phase to address:**
Phase 3 (gateway v1) — define streaming failover policy as a first-class design decision. Phase 5 (tool calling support) — extend to tool-call idempotency.

---

### Pitfall 4: Vast.ai host-private pod pulled mid-incident, recovery is slower than failover assumed

**What goes wrong:**
Vast.ai's on-demand instances are host-private hardware and **can be stopped by the host** (maintenance, hardware issue, or the host leaves the network). Interruptible instances explicitly [can be outbid and paused](https://vast.ai/article/Rental-Types). You thought you were on on-demand. Host scheduled a reboot. Primary dies. Gateway fails over to OpenRouter (correct). Provisioner tries to spin an emergency pod — **same host still selected by default search**, unavailable. Scheduler falls back to a cheaper host in a region with slow Hugging Face mirrors. Pod enters "loading" state, pulls the 20 GB custom image over a slow link for 40+ minutes, [billing continues during pull](https://docs.vast.ai/quickstart). During this window the gateway is on OpenRouter burning $X per million tokens.

Meanwhile: the provisioner logic thinks the emergency pod is "starting, ~5 min." At minute 30, operator alert finally fires. Spend already significant.

**Why it happens:**
- Vast.ai marketplace is heterogeneous — host capabilities, network links, and disk speeds vary dramatically.
- Image pulls on uncached hosts [take 10–60 minutes for a 10–20 GB image](https://docs.vast.ai/quickstart). Most dev-time tests use cached hosts.
- SSH/TLS setup adds additional delay after pull.
- The provisioner's "pod created" status does not mean "serving requests."

**How to avoid:**
1. **Build on top of Vast.ai's official CUDA base images** so most layers are pre-cached on hosts. Keep custom layers minimal (< 2 GB delta). Models downloaded from Hugging Face at startup, not baked into the image, so image size stays small.
2. **Pre-place model weights** on an S3-compatible bucket with high throughput (R2 or DO Spaces); pod startup script downloads from there, not Hugging Face, which is rate-limited and variable.
3. **Filter Vast.ai search** to hosts with `verified=true`, `disk_bw > X`, `inet_down > X Mbps`. Make the provisioner query use these constraints. Document "acceptable host profile" as part of the provisioner config.
4. **Warm start probe**: provisioner considers the pod "ready" only after a successful `/health` HTTP call to the three model endpoints, not when the container is `running`. Track elapsed from bid-accepted to ready — typical should be 3-8 min with pre-placed weights.
5. **Time-bound the provisioning attempt**: if not ready in 10 min, destroy pod, pick a different host, retry once. After second failure, alert and stay on OpenRouter.
6. **Don't rely on the same host as primary** for emergency; select from a distinct host ID.
7. Set `max_bid` cap explicitly — [Vast.ai pricing fluctuates](https://vast.ai/article/Rental-Types); a bid accepted at $0.40/h can get outbid later if you went interruptible.

**Warning signs:**
- Emergency pod provisioning time p50 > 10 min.
- Image pull time > 5 min in provisioner logs (should be < 2 min on a cached host).
- OpenRouter cost during incident climbs above expected — your mean-time-to-standby-ready is too long.
- Pod enters "exited" within 30 min of creation (host maintenance during pull).

**Phase to address:**
Phase 6 (auto-provisioning). Dedicated test plan: simulate primary-down, measure time-to-ready of emergency pod across 5 separate attempts.

---

### Pitfall 5: Spin-loop — primary recovers while emergency pod is still provisioning; you now pay for two GPUs

**What goes wrong:**
Health check declares primary dead at t=0. Provisioner starts emergency pod. At t=3 min, primary recovers (was a transient network blip). Gateway happily routes to primary again. At t=6 min, emergency pod finally becomes ready. Gateway never uses it. Pod bills at $0.35-0.40/h until someone notices (or the 5-min grace + 5-min stability timer decides to shut it down — **assuming that timer is wired in**).

Worse variant: gateway is running 2 instances (HA). Both detect primary-down simultaneously. Both call the Vast.ai API to create an emergency pod. **Two emergency pods now exist.** Your max-1-emergency-pod guardrail checked local state, not a distributed lock.

**Why it happens:**
- Health check thresholds tuned for sensitivity (failover in 30s) are too fast for provisioning timescales (5-10 min).
- Multi-instance gateway without coordination causes classic race. The [Redlock pattern](https://redis.io/docs/latest/develop/clients/patterns/distributed-locks/) or a single-writer reconciler is the fix.
- Provisioner doesn't check "is primary back healthy?" before committing to the spin-up.

**How to avoid:**
1. **Gate provisioning on sustained failure**: primary must be down for ≥ N min (e.g., 3 min, not 30s) before spin-up starts. A 30s failure triggers only "shed to OpenRouter," not "provision." Two separate thresholds.
2. **Redis distributed lock** around "start provisioning emergency pod" — acquire lease with TTL longer than typical provision time. Only one gateway instance provisions at a time.
3. **Cancel-in-flight**: during provisioning, continuously re-check primary health. If primary recovers for ≥ 2 min while pod is still `loading`, destroy the emergency pod before it finishes starting. Accept the partial cost.
4. **Single reconciler pattern** (as opposed to concurrent reactive logic): one goroutine (on the lock-holding instance) owns the state machine: `primary_healthy → primary_degraded → primary_down → provisioning → standby_ready → grace → teardown`. Transitions guarded by dwell-time.
5. **Track "phantom pods"**: every 1 min, list all active Vast.ai pods. If count > configured max (1), alert + destroy oldest. Acts as guardrail against any race.

**Warning signs:**
- Billing dashboard shows 2+ Vast.ai pods billed simultaneously.
- Emergency pod ready time logged, then a "teardown" within 5 min without ever serving traffic.
- Two gateway instances both logging `provisioning emergency pod` within the same minute.

**Phase to address:**
Phase 6 (auto-provisioning) — require lock + single-reconciler design. Phase 7 (multi-instance gateway / HA), if it exists, must re-validate.

---

### Pitfall 6: Tokenizer / context-window / tool-calling schema drift between local Qwen and OpenRouter Qwen

**What goes wrong:**
"It's the same model, Qwen 3.5 27B. Failover is invisible." Not quite.
- **Context window**: local runs at `max_model_len=16384` (VRAM-constrained). OpenRouter's Qwen 3.5 27B may offer 32k+ — [max_tokens handling differs across providers](https://github.com/danny-avila/LibreChat/discussions/9686). App sends 20k-token prompt. Local rejects with 400 (truncation). OpenRouter accepts. App works on failover, fails on primary. Debug hell.
- **Tool-call format**: local llama.cpp emits tool calls in Hermes style by default (Qwen's native chat template). OpenRouter may wrap/unwrap via a shim, occasionally [emitting raw text instead of tool-call objects](https://github.com/RooCodeInc/Roo-Code/issues/6630) — especially with Qwen routed through smaller providers on OpenRouter.
- **Tokenizer count**: same model name, different binaries; small tokenizer differences exist. If your app counts tokens for billing/quota locally using a different tokenizer than OpenRouter accounts with, **token counts won't reconcile** with OpenRouter's billing.
- **Sampling defaults** (temperature, top_p, min_p defaults) may differ. Output style subtly different post-failover.

**Why it happens:**
- "Same model" is a marketing statement about weights, not about serving layer. Quantization (Q4_K_M locally) is applied to weights on top, further drifting outputs.
- OpenRouter is a meta-provider; the actual backend may be Together, Fireworks, DeepInfra, etc. with different shims and rate limits.
- Tool-calling support across Qwen providers is [known to be inconsistent](https://github.com/musistudio/claude-code-router/issues/409).

**How to avoid:**
1. **Normalize max context at the gateway**: advertise `max_context = min(local, OpenRouter) = 16384`. Reject requests over that limit regardless of backend. Clients never see inconsistent behavior.
2. **Test the exact OpenRouter provider**: pin the OpenRouter routing to a single upstream (Together or Fireworks) and test its tool-call behavior against local llama.cpp. Document deltas.
3. **Token counting**: count tokens using local tokenizer at the gateway and use that count for billing/quota. Ignore OpenRouter's count for internal accounting (use it only to reconcile cost externally).
4. **Tool-call validation at gateway**: when stream=true with tools, buffer the tool-call JSON and validate schema before releasing to client. If malformed, fall through to plain-text response or return 502.
5. **Integration test both paths**: CI runs the same request against local and OpenRouter, diffs responses. Not for exact match (LLMs are non-deterministic), but for schema/shape.
6. **Disable failover for tool-using requests initially**: tool calling is higher-stakes; on primary failure, return a retryable 503 rather than silently failing over. Phase in tool-call failover once OpenRouter path is well-tested.

**Warning signs:**
- Users complain "responses feel different sometimes."
- Tool-call parsing errors in client logs that correlate with failover events.
- Billing reconciliation mismatch between internal token count and OpenRouter invoice > 5%.
- Some apps work fine, one specific app fails consistently on failover (its prompts exceed one side's context).

**Phase to address:**
Phase 3 (gateway v1): normalize context limits, token counting. Phase 5 (tool calling): explicit test matrix of tool-call scenarios across providers.

---

### Pitfall 7: OpenRouter fallback rate-limited or down during the **same** incident that killed the primary

**What goes wrong:**
You built for "primary dies, OpenRouter picks up." But OpenRouter had an [actual outage on Feb 17 and Feb 19, 2026 where 80-90% of requests failed for 25+ minutes](https://openrouter.ai/announcements/openrouter-outages-on-february-17-and-19-2026). A correlated incident is possible. Worse, during widespread provider incidents (a Cloudflare or regional AWS issue), everyone's fallback is the same pool — **your spillover collides with everyone's spillover** and you hit rate limits at the fallback layer.

Also: OpenRouter rate limits are per-key. If your usage normally peaks at 100 req/s against primary and spikes to 500 req/s during failover (load-shed included), you may blow through rate limit on your OpenRouter tier.

**Why it happens:**
- Single fallback = single point of failure, just one level up.
- Rate limits are not sized for incident traffic.
- Provider-of-providers (OpenRouter) adds a layer but doesn't eliminate shared infrastructure (most LLM providers sit on AWS/GCP/Azure).

**How to avoid:**
1. **Tiered fallback chain**: Tier 1 = local Qwen. Tier 2 = OpenRouter Qwen. Tier 3 = OpenAI direct (gpt-4o-mini as cheap degraded mode) **or** Anthropic Claude Haiku. For STT: Tier 2 = OpenAI Whisper API (already planned). For embeddings: Tier 2 = OpenAI text-embedding-3-small (already planned).
2. **Test Tier 3 regularly** — synthetic probe daily so you detect silent key expiration or quota changes.
3. **Budget ceiling for fallback**: in-gateway max spend/hour across OpenRouter + OpenAI. When hit, shed lowest-priority apps first. Configured per-app tier in DB.
4. **Size rate limits for peak failover traffic**, not steady state. Contact OpenRouter support to raise tier before going to prod, not during incident.
5. **Graceful degrade**: on Tier 3, response quality will differ. Mark responses with a header `X-Served-By: fallback-tier-3` so clients can dim UI / warn users / disable features that rely on specific model behavior.

**Warning signs:**
- OpenRouter 429s in logs during incident.
- Cost per hour during incident > 5x steady state — you're going to blow the monthly budget in a day.
- Tier-3 probe hasn't fired in 24h (test is broken / fallback isn't actually reachable).

**Phase to address:**
Phase 3 (failover basics): define tier chain. Phase 4 (load-shedding): implement budget ceiling. Phase 6 (provisioning): coordinate emergency-pod provision with tier activation so Tier 3 is brief.

---

### Pitfall 8: Per-app billing inaccuracy when streams are interrupted, especially on failover

**What goes wrong:**
App A calls `/v1/chat/completions` with `stream: true`. Gateway tracks tokens as the stream flows — increments a counter per chunk. Connection drops at token 200 (client closed, gateway crashed, network). Gateway's counter is already at 200, but is the final `usage` event fired? If the increment happens in a deferred/close handler, maybe. If it happens on SSE `[DONE]`, no.

On failover mid-stream: primary billed for 200 tokens locally (zero marginal cost to you, but it counted against the quota), then OpenRouter billed for 800 tokens (real money), client got 1000 total but was charged for... what? Double? Only OpenRouter? Unclear.

**Why it happens:**
- Streaming accounting is edge-case-heavy; most gateway impls only reconcile on the final `usage` event.
- When primary is local and free, devs sometimes skip accounting there; then a failover makes cost visible but the inconsistency is hidden.
- Redis `INCR` inside a streaming handler race-conditions with connection close if not carefully ordered.

**How to avoid:**
1. **Account tokens on emission, not on completion.** Every SSE chunk increments the per-app Redis counter atomically. On client disconnect, counter reflects reality.
2. **Persist final `usage` to Postgres** at stream end AND in a `defer` for abnormal close. Use `INSERT ... ON CONFLICT DO UPDATE` on a billing event ID.
3. **Separate local cost from external cost** in the billing model: `{ tokens_local, tokens_openrouter, tokens_openai, cost_brl }`. Local cost is fractional (amortized GPU cost / total tokens) — don't treat it as free internally if you want honest cost-per-app.
4. **Idempotency for billing writes**: each request gets a UUID; billing write keyed on UUID. Retries don't double-bill.
5. **End-to-end test**: streaming request, client disconnect at token 50, assert DB has 50 tokens billed, not 0.

**Warning signs:**
- Monthly billing reports don't reconcile (sum of per-app tokens != gateway aggregate logs).
- One app's token count seems suspiciously low — likely they're disconnecting before `[DONE]`.
- Billing reconciliation with OpenRouter invoice mismatches by >5%.

**Phase to address:**
Phase 3 (gateway v1 with auth + quotas). Phase 8 (billing/dashboard) validates reconciliation.

---

### Pitfall 9: Noisy-neighbor — one app saturates queue and starves all other apps

**What goes wrong:**
ConverseAI launches a campaign; 500 chat completions queued at once. Gateway forwards all 500 to local Qwen. llama.cpp has 4 slots. Apps like Telefonia (transcription — different model, doesn't compete for Qwen slots) should be fine, but the gateway's global in-flight counter hits its ceiling. Chat Ifix tries to send a transcription — **blocked by Qwen's queue** even though Whisper has capacity. End result: every app degrades because one app overran.

Or, all apps share a single Redis rate-limit token bucket — ConverseAI drains it.

**Why it happens:**
- Per-model queues not per-app queues is the default naive design.
- Rate limits configured globally not per-app.
- [Multi-tenant noisy neighbor requires layered limits + fair queuing](https://medium.com/@khalilsayed/system-design-multi-tenant-rate-limiting-service-32c63ade5ec7).

**How to avoid:**
1. **Per-app rate limits AND per-model limits AND per-app-per-model limits.** Three-dimensional, enforced atomically via Redis Lua script.
2. **Fair queue at gateway** (weighted by app tier). When model capacity is N, reserve a slot per app up to its SLA fraction; surplus shares remaining.
3. **Dedicated queue per model** — a Qwen queue backup doesn't block Whisper requests.
4. **Circuit per-app**: if one app overwhelms, trip its circuit, let others through. Better to degrade one app than all.
5. **Backpressure via 429 with Retry-After** — don't queue forever; return 429 when per-app limit hit. Client retries with backoff.
6. **Priority tiers** documented per app (e.g., Telefonia = real-time ligações = Tier S; Campanhas = batch = Tier B). Higher tier preempts lower during saturation.

**Warning signs:**
- One app's p95 latency spikes while others are fine (good — isolation working) OR all apps' p95 spikes together when one misbehaves (bad — no isolation).
- Redis rate-limit key count low (global) and same across apps — means you only have one bucket.
- Support reports "Chat Ifix slow" correlated in time with "ConverseAI campaign launch."

**Phase to address:**
Phase 3 (gateway v1): per-app quotas and per-model queues. Phase 4 (load-shedding): fairness policies.

---

### Pitfall 10: API key leakage — keys in frontend, in Sentry events, in git

**What goes wrong:**
Apps are internal. A dev accidentally uses the gateway API key in a Next.js client component (`process.env.NEXT_PUBLIC_GATEWAY_KEY`). Key in browser. Attacker reads, uses gateway to burn company money on OpenRouter passthrough. Or: Sentry captures a fetch error and logs the full Authorization header. Now every Ifix employee with Sentry access sees the key.

Or: an app is logging request headers at DEBUG in prod, keys end up in Grafana Loki with 30-day retention.

**Why it happens:**
- Internal apps feel "safe" → devs don't treat keys as public-exposed secrets.
- Sentry/pino by default may capture headers unless configured to scrub.
- `.env.local` for Next.js lets you mis-scope `NEXT_PUBLIC_` prefix.

**How to avoid:**
1. **Gateway keys NEVER in frontend.** Period. Apps call gateway from server-side only (API routes, worker). Document this in onboarding doc.
2. **Key rotation support in gateway from day 1**: keys have an expiry, can be revoked instantly via admin API. Emergency rotation is a < 5 min operation.
3. **Scrub request/response headers from Sentry and logs**: at Sentry init, redact `authorization`, `x-api-key`, `cookie`. At pino/slog, custom redactor.
4. **Anomaly detection on key usage**: one key hitting rate limit from many IPs = compromise signal. Alert.
5. **Per-app keys tied to specific origin/IP ranges** if feasible (at least for apps that deploy to known IPs in Portainer).
6. **Scan commits pre-push**: pre-commit hook greps for key patterns. git-secrets or similar.

**Warning signs:**
- Unusual traffic spike on one app's key from unknown IP.
- Key appears in `git log -p` output.
- Rate limit hit on a key for a tier the app shouldn't be using (internal app suddenly doing 10x more requests).

**Phase to address:**
Phase 2 (auth and multi-tenancy). Security checklist in CI gate for Phase 3 release.

---

### Pitfall 11: Sending customer personal data to OpenAI/OpenRouter on failover without LGPD basis

**What goes wrong:**
Telefonia transcribes customer calls (áudios pessoais, CPF mentioned, dados sensíveis). Primary path is local Whisper on Vast.ai — data stays on gateway + Vast.ai pod. Failover path is **OpenAI Whisper API** — data leaves Brazil, lands on US servers, subject to OpenAI's retention/use policies.

LGPD requires:
- A **legal basis** for the transfer (consent, legitimate interest, contract, etc.)
- **Informing** the titular about data handling
- For international transfers: specific safeguards

Your apps' privacy policies may name one processor (local Ifix infra) and not disclose OpenAI. If a customer exercises LGPD rights ("what did you share and with whom?"), you can't cleanly answer.

Same applies to LLM failover: an internal prompt that includes customer data routed to OpenRouter (which itself routes to Together/Fireworks) — who's the processor? Multiple.

**Why it happens:**
- Failover is treated as an implementation detail, not a data-flow decision.
- LGPD operational guidance for LLMs is still [limited compared to European authorities](https://fpf.org/blog/brazils-anpd-preliminary-study-on-generative-ai-highlights-the-dual-nature-of-data-protection-law-balancing-rights-with-technological-innovation/).
- "It's just a fallback that rarely fires" mindset; but an outage fires it for every user at once.

**How to avoid:**
1. **Data-class tagging per app**: apps declare what data class their requests carry (sensitive-personal / business / public). The gateway stores this on the API key.
2. **Per-class failover policy**: sensitive-personal requests **never** fail over to external providers — they fail with 503. The app handles degraded mode (e.g., queue transcription for later when primary returns).
3. **Contract with OpenAI/OpenRouter** on data retention: business-tier OpenAI accounts don't train on your data by default. Document this in your LGPD RoPA (Record of Processing Activities).
4. **Disclose processors in app privacy policies** — list OpenAI and OpenRouter as potential sub-processors for AI features, with fallback/contingency basis.
5. **PII redaction layer** at gateway (optional per app): regex + named-entity mask CPF, phone, email from prompts before sending to external. Lossy but reduces blast radius.
6. **Audit log**: every external-provider call has a retained record of `{app_id, timestamp, redacted_prompt_hash, provider, data_class}`. Enables rights response.

**Warning signs:**
- No data-class field on API keys in the DB schema → failover is unconditional.
- No audit table for external calls → can't answer LGPD requests.
- Privacy policy of ConverseAI or Telefonia doesn't mention OpenAI as processor.

**Phase to address:**
Phase 2 (auth/multi-tenancy): data-class field on keys. Phase 3 (failover): enforce per-class policy. Phase 10 (compliance review) before GA.

---

### Pitfall 12: Goroutine leaks and unpropagated cancellation on long-lived streaming

**What goes wrong:**
Go gateway handles a streaming request. Client disconnects mid-stream. If the upstream HTTP call to llama.cpp was **not** using the request's context, the gateway's goroutine keeps reading tokens from llama.cpp, writing to an already-closed client connection. Eventually blocks, goroutine leaks. Under load, thousands of zombie goroutines, memory grows, eventual OOM of the gateway VPS (4 vCPU, likely 8 GB RAM).

Also: HTTP client default connection pool behavior (e.g., keep-alive timeouts not tuned) can exhaust file descriptors when many streams open.

**Why it happens:**
- `http.NewRequest(...)` without `.WithContext(r.Context())` — cancellation doesn't propagate. [Classic Go gotcha](https://dev.to/serifcolakel/go-concurrency-mastery-preventing-goroutine-leaks-with-context-timeout-cancellation-best-1lg0).
- Launching a goroutine to read from a channel and writing to `http.ResponseWriter` without select-on-ctx.Done.
- Reading full response body before returning vs. piping via `io.Copy` to response writer (streams need the latter).

**How to avoid:**
1. **Always pass `r.Context()` to upstream HTTP requests**: `req = req.WithContext(r.Context())`. Enforce via lint (golangci-lint rule `contextcheck`).
2. **Structured concurrency for streaming**: use `errgroup.WithContext` with the request context; all child goroutines cancel when context cancels.
3. **select-on-ctx.Done() in any long loop**: `for { select { case <-ctx.Done(): return; case chunk := <-upstream: ... } }`.
4. **HTTP client tuned**: custom `http.Transport` with `MaxIdleConns`, `IdleConnTimeout`, `ResponseHeaderTimeout`, `DisableKeepAlives=false`. Share one `http.Client` instance, don't create per-request.
5. **Goroutine leak detection in tests**: use `goleak.VerifyTestMain(m)` in tests; CI fails if a test leaks goroutines. Go 1.26 adds a [goroutine leak profile](https://dev.to/gabrielanhaia/goroutine-leaks-in-go-the-4-patterns-and-the-new-profile-in-go-126-5e73) for prod.
6. **Expose `/debug/pprof`** behind admin auth, monitor goroutine count as a gauge.

**Warning signs:**
- `go_goroutines` gauge climbing monotonically.
- Memory RSS growing without traffic growth.
- `too many open files` errors under load.
- Latency for new requests increases as goroutines accumulate.

**Phase to address:**
Phase 3 (gateway v1): establish streaming patterns with tests. Phase 7 (observability): goroutine gauge + leak alert.

---

### Pitfall 13: Prometheus / metrics cardinality explosion from per-app × per-model × per-route labels

**What goes wrong:**
Natural-seeming metric: `ai_gateway_requests_total{app, model, route, status_code, tier}`. Multiply: 6 apps × 3 models × 5 routes × 20 status codes × 3 tiers = 5,400 series. Add `user_id` because "it'd be useful" → thousands of users × the above → hundreds of thousands of series. Prometheus RAM explodes. Query latency climbs. Dashboards time out.

[Cardinality explosion is well-documented](https://grafana.com/blog/2022/10/20/how-to-manage-high-cardinality-metrics-in-prometheus-and-kubernetes/). The 4 vCPU VPS hosting Prometheus (if self-hosted) falls over.

**Why it happens:**
- Metrics feel free at small scale.
- Labels creep in from "just in case."
- High-cardinality IDs (request_id, user_id, key_id) added as labels instead of to traces/logs.

**How to avoid:**
1. **Bound every label to < 20 values**. `app` ≤ 10, `model` ≤ 5, `route` ≤ 8, `status_code_class` (2xx/4xx/5xx) ≤ 3. Total series budget explicit.
2. **Never add**: `user_id`, `request_id`, `api_key_id`, `prompt_hash`, `message_id` as Prometheus labels. These are trace/log attributes.
3. **Use recording rules** for aggregated versions when you need cross-cutting queries.
4. **Use a TSDB with better cardinality handling** (VictoriaMetrics, Mimir) if scale grows — but first, constrain labels.
5. **Monitor `prometheus_tsdb_head_series`** — alert when > 50k.

**Warning signs:**
- Prometheus RAM > 4 GB on a 4 vCPU box.
- Query p95 > 1s on simple dashboards.
- `prometheus_tsdb_head_series` climbing monotonically.

**Phase to address:**
Phase 7 (observability): label budget documented at design time.

---

### Pitfall 14: Alert fatigue on WhatsApp/email — every blip pages the team

**What goes wrong:**
Target alert policy: "failover activated," "GPU went down," "quota exceeded." Reasonable at design. In practice:
- Failover fires for 30s glitches → 5 WhatsApps/day to Pedro.
- Quota exceed fires every time ConverseAI runs a campaign → spam.
- After week 2, the team mutes notifications. Then a real incident goes unseen for hours.

**Why it happens:**
- Alerts are designed for "would I want to know?" not "do I need to act right now?"
- No severity tiers or escalation policy.
- No auto-resolve.

**How to avoid:**
1. **Severity tiers enforced**:
   - **P0 (page, wake up)**: gateway is down, Tier 3 fallback is failing, sustained 5xx > 5 min.
   - **P1 (WhatsApp, business hours)**: primary down > 10 min, emergency pod provision failed.
   - **P2 (email/Slack, summary)**: failover activated, quota approaching, budget 80% consumed.
   - **P3 (dashboard only)**: load-shed events, per-app rate limit hits.
2. **Every alert has `for: 5m` minimum** — transient events don't page.
3. **Grouping**: 50 per-app quota alerts in 5 min → one notification "50 quota alerts, see dashboard."
4. **Auto-resolve**: alert clears when condition clears; fires "resolved" message.
5. **Runbook link in every alert**: "what do I do?" is answered before the team opens the phone.
6. **Monthly review**: count alerts per week. If > 20, tighten.

**Warning signs:**
- Same alert fires > 3x in an hour.
- Alerts without responses in the chat thread ("noted, doesn't need action").
- Team member admits they muted the alert channel.

**Phase to address:**
Phase 7 (observability and alerting). Runbook phase before GA.

---

## Technical Debt Patterns

| Shortcut | Immediate Benefit | Long-term Cost | When Acceptable |
|----------|-------------------|----------------|-----------------|
| Use a single global rate-limit token bucket instead of per-app | Faster to ship | Noisy neighbor; one app starves others; hard to retrofit | Never in prod — at minimum stub per-app in Phase 2 |
| Log full request/response bodies including streaming | Easy debugging | Log storage explodes (MB per chat); sensitive data in logs; LGPD risk | Only in dev with aggressive retention; never in prod without redaction |
| Use nvidia-smi polling from a cron for saturation signal | Simple | Sampling lag, false signals, no queue visibility | Only as supplementary signal alongside gateway-internal queue depth |
| Hard-code OpenRouter as the only fallback | Faster to ship | Single correlated failure mode; incident-during-incident | Fine for Phase 3 proof-of-concept; add Tier 3 before Phase 5 |
| Skip idempotency keys "because clients don't retry" | No work | When they do retry, double-billing and duplicate side effects | Never — add header handling in Phase 3, clients optional |
| Billing increment on `[DONE]` only, not per chunk | Simpler code | Disconnected streams billed zero; untrustworthy cost data | Never — per-chunk from Phase 3 |
| Single gateway instance (no HA) | No distributed-lock complexity | Gateway becomes SPOF for all AI traffic | Acceptable for Phase 1-3 MVP; HA becomes mandatory once apps depend on it |
| Detect saturation by GPU-util threshold only | One line of code | Flapping, false failover, OpenRouter cost blowup | Never — composite signal from Phase 4 |
| Bake model weights into Docker image | Simple deploy | 20+ GB image; 30-60 min pull on cold Vast.ai host; provisioning too slow | Never for emergency pod; acceptable only for primary if image is pre-cached on specific host |

---

## Integration Gotchas

| Integration | Common Mistake | Correct Approach |
|-------------|----------------|------------------|
| Vast.ai API (provisioner) | Assume pod `running` means ready-to-serve | Poll internal `/health` endpoint on each model (LLM, STT, embed) before marking ready |
| Vast.ai API (host selection) | Accept cheapest host | Filter by `verified=true`, `disk_bw`, `inet_down`, distinct from primary host |
| Vast.ai (image pull) | Build custom 20 GB image with weights baked | Build small (< 2 GB) image on top of vast-ai/base-image; download weights from S3 at start |
| OpenRouter Qwen 3.5 27B | Treat as drop-in for local Qwen | Pin upstream provider; test tool-calling behavior; normalize context window |
| OpenAI Whisper API | Expect same timing characteristics as local | API adds latency; may need different client timeout; file upload size limits differ from local |
| OpenAI Embedding API | Use `text-embedding-3-small` interchangeably with BGE-M3 | Different dims (1536 vs 1024); different semantics; don't mix in same vector index |
| Redis (for locks) | Use SETNX with long TTL | Use Redlock with TTL slightly > max provisioning time, refresh lease periodically |
| Redis (for rate limits) | Separate GET/INCR | Lua script for atomic check-and-increment |
| Postgres (shared DO Postgres) | Create tables in public schema | Dedicated schema `ai_gateway`; explicit role with minimal grants |
| llama.cpp server | Rely on default `-np 1` | Size `-np` to target concurrency; measure VRAM per slot |
| faster-whisper | Process arbitrarily long audio in one call | Enforce max audio length (e.g., 30 min); chunk longer audio |
| Sentry | Auto-capture request headers | Configure `denyList` for `authorization`, `x-api-key`, `cookie` |
| Prometheus | Use `request_id` / `user_id` as labels | Use as trace/log attributes only |

---

## Performance Traps

| Trap | Symptoms | Prevention | When It Breaks |
|------|----------|------------|----------------|
| llama.cpp slot exhaustion with `-np` too low | Queue forming, p95 TTFT climbing, GPU util < 60% | Size `-np` to target concurrency; monitor in-flight vs slot count | When concurrent chats > configured -np (4-8 typical for Qwen 27B on 4090) |
| Whisper VRAM spike on long audio | OOM every N transcriptions; inconsistent failures | Enforce max audio length; chunk long audio; bound batch size | When audio > 10 min + other models loaded |
| Gateway VPS 4 vCPU saturation from SSE streaming | Client TTFT high even though GPU is fast | Share upstream HTTP client; tune transport pool; benchmark with realistic concurrent streams | When streams concurrent > ~200 on 4 vCPU (depends on goroutine behavior) |
| Redis round-trip per rate-limit check | Gateway latency climbs under load; Redis CPU spikes | Pipeline + Lua script batched check-and-increment; local token bucket with sync | When request rate > ~1000 req/s per gateway instance |
| Image pull time for emergency pod | Long MTTR, OpenRouter cost blowup during incidents | Small image + S3 weight download; test pulls on uncached hosts | Every cold start — Vast.ai hosts vary wildly |
| Pre-allocating KV cache at model start | Fine at start, OOM under concurrent long contexts | Size `ctx-size × -np × bytes_per_token` explicitly; leave 3 GB headroom | Concurrent long-context traffic (3+ chats with 10k+ tokens) |
| Prometheus scraping cardinality bomb | Slow dashboards, OOM Prometheus | Label budget; recording rules | ~50k series on 8 GB Prometheus |
| Single fallback tier | Correlated failures; rate limit at fallback | Tiered chain: OpenRouter → OpenAI → Anthropic | During provider-wide outages (Feb 2026 OpenRouter incident pattern) |
| Synchronous billing write per request | Write amplification on Postgres | Batch billing writes; async with in-memory queue + periodic flush | Throughput > 100 req/s sustained |

---

## Security Mistakes

| Mistake | Risk | Prevention |
|---------|------|------------|
| Gateway API key used in Next.js client component | Key exposed to any user inspecting browser; unlimited abuse | Server-side only; document in onboarding; lint for `NEXT_PUBLIC_` prefix on key envs |
| Authorization header logged by Sentry/pino | Key leak via observability platform | Configure redactor/denyList at SDK init; audit log samples |
| Same gateway key shared across all apps | One app compromise = all apps compromised | One key per app; per-app revocation |
| Long-lived API keys with no rotation | Compromise has indefinite blast radius | Key expiry field; rotation via admin API; key generation logs |
| Customer PII sent to OpenAI on failover without LGPD basis | Regulatory violation; ANPD sanctions; reputational damage | Data-class tagging per app; block external failover for sensitive-personal class; disclose processors in privacy policies |
| No audit trail for external provider calls | Can't answer LGPD data-subject rights request | `external_provider_calls` audit table with retention policy |
| Gateway accepts requests from internet (not VPN/internal) | Key theft = anyone on internet can abuse | Bind gateway to Tailscale/internal IP; OR enforce origin/IP allowlist per key |
| Emergency pod creates without max-bid cap | Price spike on Vast.ai = runaway cost | Enforce `max_bid` on every provision call; separate max daily spend check |
| SSH keys for Vast.ai stored in gateway env | Gateway compromise = pod access | Use Vast.ai API keys scoped narrowly; SSH keys only in provisioner, rotated |
| Plaintext API keys in Postgres | DB dump = all keys compromised | Hash keys (bcrypt or argon2); store hash, match on auth |
| Prompt / response content stored in Sentry events | Conversation content in error tracker; LGPD risk | Scrub or hash prompts in error reports; link to secure log store instead |

---

## UX Pitfalls

| Pitfall | User Impact | Better Approach |
|---------|-------------|-----------------|
| Streaming failover produces scrambled / doubled output | Confusing chatbot responses; trust broken | Fail-fast on mid-stream primary death; client re-initiates with clear error |
| Silent failover with no signal to client app | Client can't adapt (e.g., disable tool UI when fallback has weaker tool support) | `X-Served-By` response header; client logic branches on it |
| Gateway returns 500 instead of 503/429 when over quota | Client treats as bug, not retryable | Proper status codes: 429 for rate limit, 503 with Retry-After for saturation, 402 for quota exceeded |
| Quota exceeded returns generic error | App dev can't diagnose | Structured error response: `{error: {code: "quota_exceeded", app: "...", reset_at: "..."}}` |
| Long emergency-pod provisioning feels like silent outage to apps | Apps retry aggressively, amplify load | `Retry-After: 120` on fallback responses during provisioning; back-pressure aligned with recovery timeline |
| Dashboard shows gateway metrics but not per-app breakdown | Ops team can't tell which app to call | Per-app latency/error/cost panels as first-class in dashboard |
| Alerts arrive via WhatsApp in Portuguese mixed with English stack traces | Hard to parse on mobile at 3am | Templates with runbook link, clear severity, no raw stack traces — those live in Sentry link |

---

## "Looks Done But Isn't" Checklist

Items that pass happy-path tests but will hurt in prod.

- [ ] **Failover implementation**: Often missing streaming-mid-response handling — verify what happens when primary dies at token 200 of a streaming response; also verify tool-call interruption behavior.
- [ ] **Auto-provisioning**: Often missing the "primary recovered while provisioning" cancel path — verify by injecting primary recovery at t=provision-start + 2 min.
- [ ] **Multi-tenant rate limiting**: Often missing per-app-per-model 3D limits — verify ConverseAI campaign doesn't starve Telefonia transcription.
- [ ] **Billing**: Often missing disconnection accounting — verify client-disconnect-at-token-50 results in 50 tokens billed, not 0 or full.
- [ ] **Cost ceiling**: Often missing upper-bound on fallback spend per hour — verify behavior when OpenRouter hourly cost exceeds cap.
- [ ] **VRAM guardrails**: Often missing pre-request token caps — verify that two concurrent 15k-token chats don't OOM while single-request test passes.
- [ ] **Saturation detection**: Often missing hysteresis — verify no flapping during 60s of oscillating load.
- [ ] **Circuit breaker**: Often missing half-open probe concurrency limit — verify that when primary comes back, only N test requests go through, not a flood.
- [ ] **Goroutine hygiene**: Often missing `goleak.VerifyTestMain` — verify leak tests exist and run in CI.
- [ ] **API key rotation**: Often missing rotation without downtime — verify rotate-key flow allows old and new valid for overlap period.
- [ ] **LGPD**: Often missing data-class-aware routing — verify sensitive requests never leave Vast.ai on failover.
- [ ] **Observability**: Often missing alert auto-resolve — verify alerts clear when condition clears, not just when manually acknowledged.
- [ ] **Image-pull time on cold Vast.ai host**: Often only tested against warm hosts — force a fresh host with no cached layers and time the flow.
- [ ] **Tool calling on failover**: Often only tested on primary — test against OpenRouter Qwen with the same tool schemas as primary.
- [ ] **Context window parity**: Often only tested at short contexts — send a 15k-token prompt and verify behavior across primary, OpenRouter, and failover.
- [ ] **Redis atomicity**: Often missing Lua — verify concurrent rate-limit increments don't allow over-use (hammer test with 1000 concurrent keys).
- [ ] **Phantom pod guardrail**: Often missing — simulate a stuck provisioning and verify the cleanup job destroys it within 15 min.
- [ ] **Dashboard shows data during incident**: Often only tested with all backends healthy — verify dashboard loads and shows data when primary is down.

---

## Recovery Strategies

| Pitfall | Recovery Cost | Recovery Steps |
|---------|---------------|----------------|
| VRAM OOM on Qwen | LOW | Gateway detects server 5xx, reduces `-np` and restarts llama.cpp; shed to OpenRouter during restart |
| Whisper batch OOM | LOW | Restart Whisper server; chunk pending long-audio requests |
| Spin-loop (2 emergency pods) | LOW | Phantom-pod guardrail destroys oldest; manual verification of daily spend; post-mortem on lock failure |
| Streaming failover corruption | MEDIUM | Post-incident: audit affected requests; notify affected apps; compensate for duplicate tool-call side effects (e.g., dedupe emails sent) |
| OpenRouter outage during incident | HIGH | Switch manually to Tier 3 (OpenAI direct); raise feature-flag to disable tool-call (weaker OpenAI support for Qwen-style); comms to apps |
| Tokenizer / context mismatch causing app to fail on primary | MEDIUM | Identify offending app via logs; lower gateway `max_context` advertised; app reduces prompt size; roll forward with normalized limit |
| API key leak in logs / git | HIGH | Rotate all keys immediately; audit Sentry for past leaks; review git history; scrub where possible; notify data-protection officer |
| Runaway cost on Vast.ai (phantom pods) | MEDIUM | Kill all non-primary Vast.ai instances; review billing; refund attempt (Vast.ai unlikely but worth asking); post-mortem on guardrail failure |
| LGPD data leak via failover to external | HIGH | Block external failover for sensitive class; document incident per ANPD rules (72h if data breach); contact legal; update privacy policies |
| Goroutine leak → gateway OOM | MEDIUM | Rolling restart gateway; deploy fix; add goleak tests; backfill missing billing from logs if any writes were lost |
| Alert fatigue → missed real incident | MEDIUM | Post-mortem; tighten alert thresholds; introduce severity tiers if not already; monthly review process |
| Emergency pod stuck loading 40 min | LOW | Destroy pod manually; provision on different host; kick off Vast.ai support ticket; review image size and pre-cache strategy |

---

## Pitfall-to-Phase Mapping

| Pitfall | Prevention Phase | Verification |
|---------|------------------|--------------|
| VRAM budget collapse under concurrent load | Phase 1 (GPU stack bring-up) | Load test with N concurrent 8k-token chats + 2 Whisper transcriptions, measure peak VRAM — must stay < 21 GB |
| GPU-util threshold false failover | Phase 4 (load-shedding and saturation) | Synthetic load generator, verify shed-to-external activates only when queue forming + p95 over threshold, with hysteresis (no toggle within 60s) |
| Streaming failover corruption | Phase 3 (gateway v1) | Chaos test: kill llama.cpp at token 200 of a stream, verify client sees clean error or annotated provider switch — no scrambled content |
| Vast.ai cold pull / provisioning delay | Phase 6 (auto-provisioning) | Provision emergency pod on randomly-selected uncached Vast.ai host 3x; record time-to-ready; must be p90 < 10 min |
| Spin-loop with 2 emergency pods | Phase 6 (auto-provisioning) | Chaos test: inject primary recovery at t=2 min into provision; assert pod is destroyed before serving; multi-instance gateway test for lock correctness |
| Tokenizer/context-window drift | Phase 3 (gateway v1), Phase 5 (tool calling) | Integration test matrix: same prompt to primary and fallback, assert same shape (tool call / text); 15k-token prompt test |
| Fallback rate-limit / correlated outage | Phase 3 (failover), Phase 4 (load-shedding) | Synthetic test: disable OpenRouter, verify Tier 3 activates; monthly Tier 3 probe |
| Per-app billing inaccuracy on streams | Phase 3 (gateway v1), Phase 8 (billing/dashboard) | Unit tests for per-chunk accounting; disconnection tests; monthly reconciliation automated against OpenRouter/OpenAI invoices |
| Noisy neighbor starvation | Phase 3 (gateway v1), Phase 4 (load-shedding) | Load test: one app sending 500 req/s, verify other apps' p95 < SLA |
| API key leakage | Phase 2 (auth/multi-tenancy), CI gate | Pre-commit hook greps for key patterns; Sentry redactor test; key rotation drill (< 5 min) |
| LGPD — PII to external provider | Phase 2 (multi-tenancy with data-class), Phase 3 (failover policy per class) | Test: sensitive-class request + primary down → 503, not external call; audit log entry exists |
| Goroutine leaks | Phase 3 (gateway v1), Phase 7 (observability) | `goleak.VerifyTestMain` in all tests; prod goroutine gauge < baseline for 24h of traffic |
| Cardinality explosion | Phase 7 (observability) | Label budget doc; `prometheus_tsdb_head_series` alert at 50k; review during each phase add |
| Alert fatigue | Phase 7 (observability + runbook) | Monthly alert-count review; no > 20/week per person; severity tier adherence |
| Tool-calling cross-provider failure | Phase 5 (tool calling) | Integration test: same tool schema against local + OpenRouter Qwen; assert semantic equivalence or explicit policy to disable tool-calling on failover |

---

## Sources

### Primary Incident Post-Mortems
- [OpenRouter Outages on February 17 and 19, 2026](https://openrouter.ai/announcements/openrouter-outages-on-february-17-and-19-2026) — caching failure cascade
- [Systemoverflow: Circuit Breaker Failure Modes (Flapping, Stampedes, Retry Amplification)](https://www.systemoverflow.com/learn/resilience-patterns/circuit-breaker/circuit-breaker-failure-modes-flapping-stampedes-and-retry-amplification)

### Official Documentation
- [Vast.ai Quickstart — image pull 10-60 min warning](https://docs.vast.ai/quickstart)
- [Vast.ai Rental Types — interruptible vs on-demand](https://vast.ai/article/Rental-Types)
- [Vast.ai base-image GitHub — cached layer strategy](https://github.com/vast-ai/base-image)
- [OpenRouter Rate Limits docs](https://openrouter.ai/docs/api/reference/limits)
- [OpenRouter Tool Calling docs](https://openrouter.ai/docs/guides/features/tool-calling)
- [vLLM Conserving Memory docs](https://docs.vllm.ai/en/latest/configuration/conserving_memory/)
- [vLLM Tool Calling docs](https://docs.vllm.ai/en/latest/features/tool_calling/)
- [Redis Distributed Locks pattern](https://redis.io/docs/latest/develop/clients/patterns/distributed-locks/)
- [NVIDIA nvidia-smi docs (sampling interval 1/6s)](https://docs.nvidia.com/deploy/nvidia-smi/index.html)

### GitHub Issues (active/recent)
- [vLLM #16118 — estimate max-model-len when KV cache insufficient](https://github.com/vllm-project/vllm/issues/16118)
- [vLLM #34076 — KV cache memory bottleneck calculation bug (Feb 2026)](https://github.com/vllm-project/vllm/issues/34076)
- [vLLM #27017 — KV cache memory allocations documentation](https://github.com/vllm-project/vllm/issues/27017)
- [llama.cpp discussion #18488 — memory usage grows per conversation](https://github.com/ggml-org/llama.cpp/discussions/18488)
- [claude-code-router #409 — qwen3-coder via OpenRouter 404 no tool use](https://github.com/musistudio/claude-code-router/issues/409)
- [Roo-Code #6630 — Qwen3 Coder raw text for thinking/tool-calls on OpenRouter](https://github.com/RooCodeInc/Roo-Code/issues/6630)
- [Qwen3.5 #12 — Qwen 3.5 Plus MCP Tool Calling Failure](https://github.com/QwenLM/Qwen3.5/issues/12)
- [LibreChat #9686 — max_tokens behavior mismatch across OpenRouter providers](https://github.com/danny-avila/LibreChat/discussions/9686)

### Deep-Dive Articles (2026)
- [How We Handle LLM Provider Failover at Scale — LLM Gateway blog](https://llmgateway.io/blog/how-we-handle-llm-provider-failover)
- [Portkey: Retries, Fallbacks, and Circuit Breakers in LLM apps](https://portkey.ai/blog/retries-fallbacks-and-circuit-breakers-in-llm-apps/)
- [Maxim: Retries, Fallbacks, and Circuit Breakers in LLM apps — Production Guide](https://www.getmaxim.ai/articles/retries-fallbacks-and-circuit-breakers-in-llm-apps-a-production-guide/)
- [Grafana: How to manage high cardinality metrics](https://grafana.com/blog/2022/10/20/how-to-manage-high-cardinality-metrics-in-prometheus-and-kubernetes/)
- [Goroutine Leaks in Go: The 4 Patterns and Go 1.26 Profile](https://dev.to/gabrielanhaia/goroutine-leaks-in-go-the-4-patterns-and-the-new-profile-in-go-126-5e73)
- [Leapcell: High-Performance Structured Logging with slog and zerolog](https://leapcell.io/blog/high-performance-structured-logging-in-go-with-slog-and-zerolog)
- [System Design: Multi-Tenant Rate Limiting Service (Medium, Feb 2026)](https://medium.com/@khalilsayed/system-design-multi-tenant-rate-limiting-service-32c63ade5ec7)
- [LocalLLM.in: llama.cpp VRAM Requirements 2026](https://localllm.in/blog/llamacpp-vram-requirements-for-local-llms)
- [Modal: Choosing between Whisper variants](https://modal.com/blog/choosing-whisper-variants)
- [Mobius ML: Speeding up Whisper (ASR) batching](https://mobiusml.github.io/batched_whisper_blog/)
- [AssemblyAI: LLM Gateway Guide](https://www.assemblyai.com/blog/llm-gateway)

### LGPD / Compliance
- [FPF: Brazil's ANPD Preliminary Study on Generative AI](https://fpf.org/blog/brazils-anpd-preliminary-study-on-generative-ai-highlights-the-dual-nature-of-data-protection-law-balancing-rights-with-technological-innovation/)
- [LGPD Brazil Data Protection Compliance Guide (SaaS)](https://complydog.com/blog/brazil-lgpd-complete-data-protection-compliance-guide-saas)
- [Secure Privacy: Privacy Risks in LLMs - Enterprise AI Governance](https://secureprivacy.ai/blog/privacy-risks-llms-enterprise-ai-governance)

### Go-specific
- [Go Context, Timeout, Cancellation Best Practices](https://dev.to/serifcolakel/go-concurrency-mastery-preventing-goroutine-leaks-with-context-timeout-cancellation-best-1lg0)
- [Better Stack: Go Logging Benchmarks](https://betterstack-community.github.io/go-logging-benchmarks/)

---

*Pitfalls research for: AI inference gateway (Go) with failover + auto-provisioning — Ifix Telecom*
*Researched: 2026-04-17*
