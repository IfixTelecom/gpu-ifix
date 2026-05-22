---
status: root_cause_found
trigger: "Phase 06.6 UAT 14 + UAT 15 surfaced tech debt #4: gateway POST /v1/chat/completions returns 503 service_unavailable / upstream_unavailable / message 'Upstream proxy not registered' even after loader.OverrideTier0('llm', podURL) logs success. Direct curl to the live Vast pod returns 200 with system_fingerprint=b9191-*; the gap is gateway-side dispatch wiring. STATE narrative blamed the probe loop reading UPSTREAM_LLM_URL (placeholder http://172.18.0.1:18000 in prod-dev env) instead of the override URL, but static analysis of dispatcher.go Resolve/dispatchTo path did not fully explain how the 'Upstream proxy not registered' message gets emitted — emergency_pod_llm IS registered in llmRoleProxies at boot in current HEAD (main.go:622). Must reproduce live and isolate the exact path."
created: 2026-05-22
updated: 2026-05-22
goal: find_root_cause_only
---

# Debug Session: primary-override-dispatch-503

## Current Focus

reasoning_checkpoint:
  hypothesis: |
    SETTLED — see Resolution.root_cause. Tech debt #4 is a HISTORICAL bug
    observed on the UAT 14 deploy of develop tip af81836 (parent
    bda05fb on main.go). At that point markReady called
    OverrideTier0("llm"/"stt"/"tts", podURL), Resolve returned the
    synthetic name "emergency_pod_<role>", and the dispatcher looked up
    cfg.Proxies["emergency_pod_<role>"] — but llmRoleProxies and
    sttRoleProxies on bda05fb had only the {"local-llm"} and
    {"local-stt"} keys plus optional openrouter/whisper fallbacks. There
    was no "emergency_pod_*" entry. dispatchTo therefore fell into the
    !ok branch (dispatcher.go:325-329) and emitted exactly the observed
    503 envelope. Direct pod curl worked because it bypassed the
    gateway entirely.

    The latent gap was already closed BEFORE this debug session opened.
    Fixes shipped in commits:
      - 12f7479 fix(06.7-07): register dynamic emergency_pod_tts proxy
      - 30f90e7 fix(06.7): register dynamic emergency_pod_{llm,stt} proxies
    Both are ancestors of current HEAD (1e3c62e) and present in the
    deployed image SHA d6893211 (latest-dev) running on vps-ifix-vm.

  next_action: |
    Per goal=find_root_cause_only and the operator's --diagnose
    constraint: STOP. Root Cause Report emitted; no source-file edits;
    no Vast spend.

    Optional next steps the operator may pursue separately (out of
    scope for this session):
      A. Re-run UAT 14 gateway path on current HEAD (1e3c62e) with a
         real Vast pod to convert tech debt #4 in STATE.md from "open"
         to "closed — fixed by 30f90e7 + 12f7479 + reverified on
         <date>".
      B. Add a regression test in
         gateway/internal/proxy/dispatcher_test.go that boots the
         dispatcher with llmRoleProxies missing "emergency_pod_llm"
         and asserts the exact 503 envelope — protects against future
         drift if a refactor drops the registration.

  expecting: |
    No further investigation. Findings already isolate to a single
    failing code path (dispatcher.go:323-329 dispatchTo !ok branch
    reached because llmRoleProxies omitted "emergency_pod_llm" at boot
    on the UAT 14 deploy commit bda05fb). Live reproduction is
    impossible on current HEAD without rolling back to bda05fb
    (counterproductive).

  confirming_evidence:
    - id: E1
      kind: code_inspection
      file: gateway/internal/proxy/dispatcher.go
      lines: "323-329"
      proves: |
        The literal error string "Upstream proxy not registered." is
        emitted ONLY by dispatchTo when cfg.Proxies[name] is missing.
        Grep across the gateway tree confirms exactly one emit site.
    - id: E2
      kind: code_inspection
      file: gateway/internal/upstreams/loader.go
      lines: "205-237"
      proves: |
        Resolve(role, 0) with tier0Override active returns
        UpstreamConfig{Name: "emergency_pod_" + role, ...}. The
        dispatcher then calls dispatchTo(t0.Name, ...) which is
        dispatchTo("emergency_pod_llm", ...).
    - id: E3
      kind: git_archaeology
      ref: bda05fb (UAT 14 deploy, parent of af81836)
      file: gateway/cmd/gateway/main.go
      lines: "600-635"
      proves: |
        llmRoleProxies at bda05fb contained ONLY {"local-llm"} plus
        optional "openrouter-chat". No "emergency_pod_llm" key.
        sttRoleProxies contained ONLY {"local-stt"} plus optional
        "openai-whisper". Therefore loader.Resolve returning name
        "emergency_pod_llm" / "emergency_pod_stt" lookup-missed in
        cfg.Proxies → dispatchTo's !ok branch fired → 503 "Upstream
        proxy not registered."
    - id: E4
      kind: git_archaeology
      ref: 30f90e7 + 12f7479
      proves: |
        Two follow-up commits explicitly close the gap. 30f90e7 commit
        message verbatim: "llm + stt resolve to 'emergency_pod_<role>'
        when the reconciler overrides the tier-0 with the live pod URL,
        but no proxy was registered under those names — so chat +
        transcription through the gateway to the primary/emergency pod
        503'd ('Upstream proxy not registered'), the same bug already
        fixed for tts. Only pod-direct calls worked." Both commits are
        ancestors of HEAD and present in deployed image d6893211.
    - id: E5
      kind: code_inspection
      file: gateway/internal/proxy/dynamic_override.go
      lines: "1-12"
      proves: |
        The file's own package-level comment documents the contract:
        "a proxy MUST be registered under that name for every
        dynamic-override role (llm, stt, tts), or the dispatcher 503s
        with 'Upstream proxy not registered'."
    - id: E6
      kind: live_inspection
      command: ssh vps-ifix-vm 'docker logs ai-gateway-dev 2>&1 | grep upstreams'
      proves: |
        Loader snapshot at boot of the deployed image holds 5 rows
        (local-llm, local-stt, local-embed, local-tts, voice-api-piper).
        openrouter-chat, openai-whisper, openai-embed were SKIPPED at
        Refresh because their url_env vars are unset in
        /opt/ai-gateway-dev/.env. So Resolve("llm", 1) returns
        (_, false) on this deploy — confirming the dispatcher's tier-1
        path returns a DIFFERENT error envelope ("Primary upstream
        unavailable and no fallback configured for role.") and is NOT
        the source of the symptom message.
    - id: E7
      kind: env_inspection
      file: vps-ifix-vm:/opt/ai-gateway-dev/.env
      proves: |
        Observed today (2026-05-22):
          UPSTREAM_LLM_URL=http://172.18.0.1:18000  (placeholder dead)
          UPSTREAM_TTS_URL=http://127.0.0.1:1      (dead)
          UPSTREAM_TTS_PIPER_URL=http://172.18.0.1:5100
        Probe loop hammering local-llm + local-stt at dead URLs will
        trip those tier-0 breakers to OPEN within 30s of boot, which
        is the secondary trigger that exposed the original bug on
        bda05fb but is ORTHOGONAL to today's fixed-HEAD behavior
        (OverrideTier0 short-circuits Resolve before the local-llm
        breaker state is consulted).

  eliminated:
    - id: H2
      claim: "Wrong proxy lookup name drift (emergency_pod_llm vs emergency-pod-llm vs primary_llm)"
      evidence: "grep across gateway/ shows only one canonical 'emergency_pod_<role>' spelling; main.go (cmd/gateway/main.go:622) + loader.go:229 + dynamic_override.go:6 all agree."
    - id: H3
      claim: "Transient request hit the gateway BEFORE OverrideTier0 fired"
      evidence: |
        Reproducible 503 across UAT 14 + UAT 15 — not a one-off race.
        Commit messages of the two fixes explicitly say 'chat +
        transcription through the gateway to the primary/emergency pod
        503'd' (steady-state, not transient).
    - id: H1-subhyp-a
      claim: "Two Loader instances (reconciler holds A, dispatcher holds B)"
      evidence: |
        main.go constructs exactly one loader (line 296ish, used at 308
        for breakerSet, 311 for probe, 624 for llmRoleProxies' Tier0OverrideURL
        closures, 773 for emerg.Reconciler.Deps.Loader, 899 for
        primary.Reconciler.Deps.Loader, 963/973/983/996 for the 4 dispatchers).
        Same pointer everywhere.
    - id: H1-subhyp-b
      claim: "Reconciler reset path cleared the override between markReady and request arrival"
      evidence: |
        startDrain → RestoreTier0 happens only on cutback (StateReady→Draining)
        and closeLifecycle. The narrative observed 503 RIGHT AFTER markReady
        published primary_ready — no drain in flight. closeLifecycle's
        RestoreTier0 is also gated on Drain having already cleared the slot.
    - id: H1-subhyp-tier1
      claim: "Resolve('llm',1) returns openrouter-chat with name registered in proxies but URL empty"
      evidence: |
        loader.Refresh skips rows whose url_env is empty (loader.go:130-136).
        With UPSTREAM_LLM_OPENROUTER_URL unset, openrouter-chat is dropped
        from the snapshot. Resolve('llm', 1) returns (_, false) →
        dispatcher.go:257-261 emits a DIFFERENT 503 envelope ('Primary
        upstream unavailable and no fallback configured for role.')
        which does NOT match the symptom string.

## Symptoms

expected: |
  POST /v1/chat/completions through gateway returns 200 with the pod's
  system_fingerprint (b9191-*) when the primary reconciler has called
  OverrideTier0('llm', podURL) and the pod is reachable (direct-probe
  returns 200).

actual: |
  Gateway returns 503 service_unavailable, OpenAI-shaped envelope with
  type='service_unavailable', code='upstream_unavailable', message
  'Upstream proxy not registered.' — even though earlier breadcrumbs show
  'tier-0 override activated (emerg) role=llm override_url=<podURL>' and
  the operator can curl the pod URL directly with 200.

errors: |
  Response body:
    {"error":{"type":"service_unavailable","code":"upstream_unavailable",
              "message":"Upstream proxy not registered."}}
  Status: HTTP/1.1 503 Service Unavailable
  Source: gateway/internal/proxy/dispatcher.go:323-329 dispatchTo

timeline: |
  - UAT 14 (2026-05-19 early, develop tip af81836; main.go from parent
    bda05fb): first observation. All 4 supervisord children RUNNING,
    override logs fired, gateway-path probe returned 503. Direct-probe to
    pod IP returned 200 with system_fingerprint=b9191-4f13cb742. Logged
    as tech debt #4 in STATE.md.
  - UAT 15 (2026-05-19 late, develop tip 9720097): not retested
    gateway-path. Drain-under-DISABLED fix (tech debts #1+#2) was the
    session focus; direct-probe used throughout.
  - UAT 16 (2026-05-19 later, develop tip 73cf914): focused on STT silero
    asset; chat_completions override path not exercised.
  - 2026-05-21 08:55 BR — fix(06.7-07) 12f7479 lands: registers
    emergency_pod_tts in ttsRoleProxies.
  - 2026-05-21 18:03 BR — fix(06.7) 30f90e7 lands: registers
    emergency_pod_llm + emergency_pod_stt in their respective maps.
  - 2026-05-22 (today, develop tip 1e3c62e, deployed image
    d6893211 = latest-dev on vps-ifix-vm): both fixes present;
    static analysis confirms the dispatcher cannot reach the failing
    dispatchTo !ok branch via the OverrideTier0 path for any of the 3
    dynamic-override roles.

reproduction: |
  ORIGINAL (UAT 14, pre-fix bda05fb):
    1. Boot ai-gateway-dev on vps-ifix-vm with bda05fb image +
       PRIMARY_POD_SCHEDULE_DISABLED=true + 24 PRIMARY_* env vars set +
       UPSTREAM_LLM_URL=http://172.18.0.1:18000 (placeholder dead) +
       UPSTREAM_STT_URL=same + UPSTREAM_EMBED_URL=http://10.10.10.20:7997 +
       UPSTREAM_TTS_URL=http://127.0.0.1:1.
    2. gatewayctl primary force-up (spends ~$0.30 on Vast 4090).
    3. Wait for log 'primary_ready observed' (~5min).
    4. Curl gateway /v1/chat/completions with valid tenant key.
    5. 503 'Upstream proxy not registered' emitted by dispatcher.go:328.
    6. Curl pod IP directly → 200 (pod is alive; only gateway routing is
       broken).

  POST-FIX (current HEAD 1e3c62e):
    Steps 1-3 same; step 4 returns 200 from the pod via dispatcher →
    NewDynamicOverrideProxy("llm", loader.Tier0OverrideURL, ...) →
    ReverseProxy.ServeHTTP. Test only after operator authorizes a Vast
    spend; no expected regression based on static analysis.

## Evidence

- timestamp: 2026-05-22T17:00:00-03:00
  source: code_grep
  command: grep -rn "Upstream proxy not registered" gateway/
  finding: |
    Only one emit site: gateway/internal/proxy/dispatcher.go:328 inside
    dispatchTo when cfg.Proxies[name] lookup fails (line 324-329).
- timestamp: 2026-05-22T17:01:00-03:00
  source: code_inspection
  file: gateway/internal/upstreams/loader.go
  lines: "205-237"
  finding: |
    Resolve(role, 0) with tier0Override active synthesizes
    UpstreamConfig{Name: "emergency_pod_" + role, URL: *overridePtr,
    IsEmergency: true, ...} and returns it. No fallback into byRoleTier
    snapshot for the name; the synthesized name is what the dispatcher
    sees as t0.Name.
- timestamp: 2026-05-22T17:02:00-03:00
  source: code_inspection
  file: gateway/internal/primary/reconciler.go
  lines: "548-579"
  finding: |
    markReady calls OverrideTier0 for "llm", "stt", "tts" in sequence
    (lines 572-574). All 3 dynamic-override roles are activated
    together on Provisioning→Ready.
- timestamp: 2026-05-22T17:03:00-03:00
  source: git_show
  ref: bda05fb:gateway/cmd/gateway/main.go
  finding: |
    llmRoleProxies := map[string]http.Handler{
        "local-llm": proxy.ToolCallTerminalGuard(chatRP, ...),
    }
    // optional openrouter-chat append
    sttRoleProxies := map[string]http.Handler{"local-stt": audioRP}
    // optional openai-whisper append
    NO "emergency_pod_llm" / "emergency_pod_stt" / "emergency_pod_tts"
    keys at this revision. ttsRoleProxies block did not exist (tts role
    added later in 06.7).
- timestamp: 2026-05-22T17:04:00-03:00
  source: git_log_diff
  ref: 30f90e7
  finding: |
    Commit diff adds "emergency_pod_llm" + "emergency_pod_stt" entries
    to llmRoleProxies + sttRoleProxies using NewDynamicOverrideProxy +
    loader.Tier0OverrideURL closures. Commit message documents the
    exact bug: "llm + stt resolve to 'emergency_pod_<role>' when the
    reconciler overrides the tier-0 with the live pod URL, but no proxy
    was registered under those names — so chat + transcription through
    the gateway to the primary/emergency pod 503'd ('Upstream proxy not
    registered'), the same bug already fixed for tts."
- timestamp: 2026-05-22T17:05:00-03:00
  source: git_ancestor_check
  command: git merge-base --is-ancestor 30f90e7 HEAD && git merge-base --is-ancestor 12f7479 HEAD
  finding: |
    Both fixes are ancestors of HEAD (1e3c62e). Static analysis on
    current main.go (lines 618-690 read in this session) confirms all
    3 emergency_pod_{llm,stt,tts} entries are unconditionally registered
    at boot.
- timestamp: 2026-05-22T17:06:00-03:00
  source: deployed_image_check
  command: ssh vps-ifix-vm 'docker inspect ai-gateway-dev --format "{{index .Config.Labels \"org.opencontainers.image.revision\"}}"'
  finding: |
    Deployed SHA = d6893211a2ed0d348e7a55253c42cb852e790f31. Both fix
    commits (30f90e7, 12f7479) are ancestors of this SHA. So the live
    image on vps-ifix-vm cannot reproduce tech debt #4's failure mode
    via the same code path.
- timestamp: 2026-05-22T17:07:00-03:00
  source: live_log
  command: ssh vps-ifix-vm 'docker logs ai-gateway-dev 2>&1 | grep upstreams'
  finding: |
    Loader Refresh at boot skipped openai-embed, openrouter-chat,
    openai-whisper (env vars unset). Final snapshot rows=5
    (local-llm, local-stt, local-embed, local-tts, voice-api-piper).
    No "openrouter-chat" present, so Resolve("llm", 1) returns
    (_, false) on this deploy — eliminates the tier-1 fallback
    sub-hypothesis as a source of the symptom string.

## Eliminated

- H2 (proxy name drift) — single canonical spelling across all 4 sites.
- H3 (transient/race) — bug was reproducible across UAT 14 + UAT 15.
- H1-subhyp-a (two Loader instances) — single loader pointer threaded
  through 8 consumers in main.go.
- H1-subhyp-b (RestoreTier0 cleared the slot prematurely) — only fires
  on cutback/close, which were not in flight at observation time.
- H1-subhyp-tier1 (tier-1 fallback to openrouter-chat) — openrouter-chat
  row was SKIPPED at Refresh on the UAT 14 deploy (same env config as
  today), so Resolve(role, 1) returned (_, false) → dispatcher emits a
  DIFFERENT envelope ("Primary upstream unavailable and no fallback
  configured for role."), not the observed "Upstream proxy not registered."

## Resolution

root_cause: |
  At the UAT 14 deploy commit (bda05fb / af81836), the gateway's static
  proxy maps llmRoleProxies + sttRoleProxies omitted the
  "emergency_pod_<role>" entries that loader.Resolve synthesizes when
  OverrideTier0 is active. ttsRoleProxies didn't even exist yet at that
  commit. The flow was:

    1. primary.Reconciler.markReady → loader.OverrideTier0("llm", podURL).
    2. Client POST /v1/chat/completions.
    3. dispatcher.Resolve("llm", 0) returns UpstreamConfig{
         Name: "emergency_pod_llm", URL: podURL, IsEmergency: true, ...
       } via loader.go:226-231.
    4. dispatcher.go:208 cfg.Breaker.Get("emergency_pod_llm") → (nil,
       false) because the breaker set was built from loader.Names()
       which does NOT include the synthesized name; t0State falls back
       to the default StateClosed.
    5. dispatcher.go:225 takes the StateClosed branch →
       cfg.dispatchTo(w, r, "emergency_pod_llm", streaming, log).
    6. dispatcher.go:324 cfg.Proxies["emergency_pod_llm"] → (nil, false)
       because the map was built at boot with only {"local-llm"} +
       optional "openrouter-chat".
    7. dispatcher.go:325-329 emits HTTP 503 with body
       {"error":{"type":"service_unavailable","code":"upstream_unavailable",
                  "message":"Upstream proxy not registered."}}.

  The same chain explained the symmetric symptom for the stt role and
  (once tts override landed) the tts role.

  The root cause is therefore a CONTRACT MISMATCH between two cooperating
  components introduced when Plan 06-08 added OverrideTier0:
    - upstreams.Loader.Resolve unilaterally renames the upstream
      "emergency_pod_<role>" when the override slot is set.
    - proxy.Dispatcher requires cfg.Proxies to contain that exact name.
    - main.go's boot-time wiring of cfg.Proxies pre-dated the renamer
      and never gained the corresponding "emergency_pod_<role>" entries.

  Direct pod curl worked because it bypassed steps 1-7 entirely.

  Exact failing line on bda05fb deploy: gateway/internal/proxy/dispatcher.go:325-329
  (dispatchTo's !ok branch), reached via the chain above.

fix: |
  not applied (find_root_cause_only / --diagnose).

  The fix has ALREADY shipped on develop and is in the live image:
    - 30f90e7 fix(06.7): register dynamic emergency_pod_{llm,stt} proxies
        adds "emergency_pod_llm" + "emergency_pod_stt" keys to the two
        maps in main.go using proxy.NewDynamicOverrideProxy(role,
        loader.Tier0OverrideURL closure, ...).
    - 12f7479 fix(06.7-07): register dynamic emergency_pod_tts proxy
        adds "emergency_pod_tts" key to ttsRoleProxies using
        proxy.NewDynamicTTSProxy(loader.Tier0OverrideURL closure, log).
    - New file gateway/internal/proxy/dynamic_override.go embeds the
      package-level contract docstring naming the bug so future readers
      see the relationship without a code-archaeology pass.

  Recommended verification step (not run in this session — requires
  operator-authorized Vast spend): rerun UAT 14 gateway path against
  current HEAD with a real primary pod and confirm 200 from the pod
  via the gateway with system_fingerprint=b9191-* in the response.
  After PASS, mark tech debt #4 closed in STATE.md with reference to
  this debug session + the two fix commits.

  Optional regression hardening (not requested): add
  gateway/internal/proxy/dispatcher_test.go case that boots the
  dispatcher with llmRoleProxies missing "emergency_pod_llm" + a
  loader stub returning the synthetic name, then asserts the 503
  envelope. Locks the contract behaviorally.
