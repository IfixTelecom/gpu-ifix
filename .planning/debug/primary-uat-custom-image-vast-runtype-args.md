---
status: root_cause_found
trigger: "8 UAT attempts on 2026-05-18 all failed: custom converseai-primary-pod image consistently exits within 30-60s of image extract on Vast.ai while the upstream llama.cpp:server-cuda-b9191 image succeeds with the same Vast CreateRequest args. Local docker run on vps-ifix-vm works perfectly. Must diagnose why Vast handles custom multi-layer image differently than upstream single-layer image."
created: 2026-05-18
updated: 2026-05-18
goal: find_root_cause_only
---

# Debug Session: primary-uat-custom-image-vast-runtype-args

## Current Focus

reasoning_checkpoint:
  hypothesis: |
    Top hypothesis after investigation — H6 (NEW):
    The custom image inherits a broken HEALTHCHECK from the
    llama.cpp:server-cuda-b9191 base image (`CMD curl -f
    http://localhost:8080/health`) AND the Dockerfile never overrides it.
    Combined with Vast.ai's worker-side container lifecycle policy, which
    likely reaps containers that are persistently `unhealthy` during the
    boot grace window, this drives `actual_status=exited` within 30-60s
    even though the bash onstart and supervisord chain would otherwise
    complete.

    Supporting facts found in the investigation:
      - Custom image: 13.1 GB, 18 layers, ENTRYPOINT=["/bin/bash"]
        OK, HEALTHCHECK={CMD curl -f http://localhost:8080/health}
        (INHERITED from llama.cpp base, NOT overridden)
      - Upstream image: 3.44 GB, 13 layers, ENTRYPOINT=[/app/llama-server],
        same HEALTHCHECK but llama-server binds 0.0.0.0:8080 by default
        when invoked from its native ENTRYPOINT → healthcheck PASSES
      - Local docker run: confirmed container stays `running` for 60+s
        even with healthcheck failing on :8080 (Docker upstream does NOT
        kill on unhealthy, only sets Status=unhealthy). Vast's worker
        policy appears to differ.
      - The 8.97 GB pip-install layer is the largest single layer; Vast
        host with weak download bandwidth might also have inflated pull
        time, but the Vast lifecycle showed `actual_status=exited` (not
        `loading` timeout) and no `status_msg` populated, suggesting the
        container DID start and was reaped post-start.

    Secondary hypothesis — H3 (image-extraction stress):
    The 8.97 GB pip-install layer + ~30 GB total disk footprint (12.5 GB
    image extracted + 17 GB Qwen GGUF download + tarballs) approach the
    50 GB disk request and may stress Vast host overlayfs or fill the
    container ephemeral disk before the bash onstart can complete the
    aria2c download phase. (Local does not have this constraint because
    we test on an 8-core, 24 GB RAM, 200 GB-disk VM.)

  confirming_evidence:
    - "image inspect on custom: Entrypoint=[/bin/bash] (OK), Healthcheck=[CMD curl -f http://localhost:8080/health] (INHERITED, INVALID for our pod), Layers=18, Size=13098 MB"
    - "image inspect on upstream: Entrypoint=[/app/llama-server], Healthcheck same, Layers=13, Size=3444 MB"
    - "docker history custom: 8.97 GB pip-install layer is the dominant size contributor; the apt-get layer is 441 MB; dcgm COPY is 76.8 MB"
    - "local docker run unhealthy reproduction: t+47s, Status=running, Health=starting, FailingStreak=1; Docker upstream does not reap unhealthy containers"
    - "vast-cli SDK source (vastai/api/instances.py:72-99): create_instance API has NO entrypoint JSON field. CLI flag --entrypoint maps to JSON `onstart` (line 161 fallback). Our payload Onstart=/bin/bash + Args=[-c, onstart] is the same shape as Vast's canonical example for runtype=args (vastai/cli/commands/instances.py:286)."
    - "phase 6 SPIKE-runtype-args Round 2: same payload shape PASSED on upstream image (lifecycle 39 GREEN). emerg lifecycle.go:794-801 comment captures: `Vast.ai API has NO entrypoint JSON field; vast-cli --entrypoint coerces into onstart_cmd`. So the wire protocol is correct."
    - "phase 6.6 SPIKE-dind-privileged Round 4: overlayfs mount in nested Vast namespace fails with `operation not permitted` (this was a DinD spike, but is the closest empirical evidence we have of overlayfs limits inside Vast containers)"
    - "gateway env on vps-ifix-vm: all 24 PRIMARY_* + 4 MINIO_* + POD_DEBUG_SSH_PUBLIC_KEY present. If the pod arrived at the bash :?required env-guards, the SSH key would be installed and we could SSH in. We cannot — pod exits before sshd starts."

  falsification_test: |
    DECISIVE EXPERIMENT (Approach A, ~$0.30 / one pod ≤10min):
      1. Build a stripped-down "diagnostic" custom image (or use the
         current :develop tag with a one-line PATCH for HEALTHCHECK NONE
         and re-push as a :diag tag — operator's choice). The image MUST
         carry `HEALTHCHECK NONE` to neutralise H6.
      2. PRIMARY_TEMPLATE_IMAGE := <diag tag>.
      3. ENV `POD_DEBUG_SSH_PUBLIC_KEY` already set on the dev gateway.
      4. Override the onstart at the gateway side (or set
         PRIMARY_QWEN_WEIGHTS_KEY = a small fake → :?required fires →
         we get a fast bash exit AND a captured status_msg if Vast had
         one).
      5. Force-up via gatewayctl. Watch for either:
         (a) Pod reaches `actual_status=running`, sshd starts → we SSH
             in → confirm H6 is the only blocker (HEALTHCHECK NONE alone
             made the pod survive).
         (b) Pod still `exited` quickly → H6 is NOT the root cause;
             escalate to H3 (disk stress) or H4 (kernel/driver).

    NON-VAST FALSIFICATION (already done in this session):
      - Local docker run confirmed: container WITH inherited healthcheck
        stays `running` 60+s. Docker engine does NOT reap unhealthy.
        ⇒ If Vast is reaping, it is Vast-specific behaviour (not Docker
        upstream).

  next_action: |
    Pending operator decision (debug session complete; report below).

## Evidence

- timestamp: 2026-05-18T17:00:00-03:00 / source: docker image inspect on
  vps-ifix-vm / finding: custom converseai-primary-pod:develop revision
  `ca514f94` is current on disk + on GHCR (RepoDigest
  sha256:754dfc66...). Config.Entrypoint=["/bin/bash"]. Cmd=None.
  WorkingDir=/app. **Healthcheck inherited from base image**: CMD curl
  -f http://localhost:8080/health. RootFS Layers count=18. Size=13.098 GB.

- timestamp: 2026-05-18T17:00:30-03:00 / source: docker history on
  vps-ifix-vm / finding: 8.97 GB pip-install layer (speaches + infinity
  venvs) is the largest layer; 441 MB apt-install layer; 76.8 MB dcgm
  binary COPY; 8.35 MB llama-server + 185 MB libs from base; 9.52 MB
  libgomp/curl install; 3.11 GB CUDA libs from nvidia/cuda. Custom image
  is **3.81× larger than upstream** (13.098 GB vs 3.444 GB) and has
  **5 more layers** (18 vs 13).

- timestamp: 2026-05-18T17:01:00-03:00 / source: docker image inspect
  ghcr.io/ggml-org/llama.cpp:server-cuda-b9191 / finding:
  Entrypoint=[/app/llama-server]. Same Healthcheck. **13 layers**, 3.444 GB.

- timestamp: 2026-05-18T17:02:00-03:00 / source: local docker run
  dry-run / finding: `docker run --rm -d --entrypoint /bin/bash <custom
  image> -c "sleep 600"` stays `Status=running, Health=starting,
  FailingStreak=1` for 60s. Healthcheck logs show `curl: (7) Failed to
  connect to localhost port 8080 after 0 ms: Couldn't connect to server`.
  Docker upstream does NOT kill the container.

- timestamp: 2026-05-18T17:03:00-03:00 / source: vastai/api/instances.py
  + vastai/cli/commands/instances.py / finding: Vast.ai create-instance
  API JSON has fields {`image`, `env`, `onstart`, `runtype`, `args`,
  `disk`, `label`, `target_state`} — **NO `entrypoint` field**. CLI flag
  `--entrypoint X` is server-side identical to `--onstart-cmd X` (line
  161: `args.onstart_cmd = args.entrypoint`). Wire payload sent by our
  gateway is byte-identical in shape to the canonical Vast example
  `--onstart-cmd 'bash' --args -c '...'` (line 286). Wire protocol is
  correct.

- timestamp: 2026-05-18T17:04:00-03:00 / source: gateway logs lifecycle
  13 (force-up at 16:09:44) / finding: 3min 21s from force-up to first
  `actual_status=exited` strike. status_msg empty. 3 consecutive strikes
  → close at 16:13:16 with shutdown_reason `instance_terminal_state`.

- timestamp: 2026-05-18T17:05:00-03:00 / source: phase 6.6-SPIKE-dind /
  finding: overlayfs mount in nested namespace fails with `operation not
  permitted` on Vast hosts (this is for DinD, but illustrates that Vast
  hosts have stricter overlayfs semantics than commodity Linux).

- timestamp: 2026-05-18T17:06:00-03:00 / source: gateway env on dev
  container / finding: All 10 required onstart env vars present
  (MINIO_* × 4, PRIMARY_QWEN_WEIGHTS_KEY+SHA256, PRIMARY_WHISPER_*+SHA256,
  PRIMARY_BGEM3_*+SHA256). POD_DEBUG_SSH_PUBLIC_KEY also set. **If the
  onstart bash had reached the sshd-install block (lines 97-107), we
  could SSH into the failing pod.** We cannot. So the pod exits BEFORE
  reaching that block (or at the latest, during apt-get install
  openssh-server).

## Resolution

Root cause hypothesis ranked + recommended fix path documented below in
the Root Cause Report (returned to operator). NO code changes applied
this session (goal = find_root_cause_only).
