# Phase 11 Plan 11-02 — Staging smoke evidence (Task 11-02-06)

**Status:** PENDING OPERATOR — gate-blocked at checkpoint 11-02-06.

This file is the evidence artifact for the BLOCKING staging smoke that
MUST be green before Task 11-02-07 (prod `bunx @better-auth/cli@latest
migrate` against `bd_ai_dashboard_prod`) runs. The executor agent has
shipped tasks 11-02-01 through 11-02-05A; this checkpoint is the next
gate.

## Operator runbook

### Pre-conditions

- Pick a staging target (record the chosen option below):
  - **Option 1** — Separate schema `dashboard_auth_staging` on the
    existing DO instance `bd_ai_dashboard_prod`. Lowest cost; isolated
    from prod data. **Recommended default.**
  - **Option 2** — Re-purpose dev DSN (`bd_ai_dashboard_dev` if it
    exists) for the smoke window.
  - **Option 3** — Ephemeral DO database fork (most isolated; highest
    cost). Use only if Options 1/2 are unavailable.
- Have available:
  - `STAGING_DSN` — Postgres DSN for the chosen target.
  - `BETTER_AUTH_SECRET` — any 32-byte random string for the smoke
    window (do NOT re-use prod secret).
  - `BETTER_AUTH_URL` — e.g. `http://localhost:3001` for a local smoke,
    or the staging dashboard URL.

### Step-by-step

```bash
# 1. Temp .env, mode 600 — never check in.
cat > /tmp/dashboard-staging.env <<'EOF'
DASHBOARD_DATABASE_URL=<STAGING_DSN>
BETTER_AUTH_SECRET=<32-byte-random>
BETTER_AUTH_URL=http://localhost:3001
DASHBOARD_ALLOWED_EMAIL_DOMAINS=ifixtelecom.com.br
EOF
chmod 600 /tmp/dashboard-staging.env

# 2. Dry-run the canonical migrate (SINGLE command, no Drizzle push).
cd dashboard
set -a; . /tmp/dashboard-staging.env; set +a
BETTER_AUTH_NO_INTERACTIVE=1 bunx @better-auth/cli@latest migrate --dry-run
# Inspect SQL — expect ALTER TABLE "user" ADD COLUMN "two_factor_enabled"
# and CREATE TABLE "twoFactor" (or two_factor) plus backup-codes columns.

# 3. Real migrate against staging.
BETTER_AUTH_NO_INTERACTIVE=1 bunx @better-auth/cli@latest migrate --y

# 4. Verify staging schema.
psql "$DASHBOARD_DATABASE_URL" -c '\d "twoFactor"'
psql "$DASHBOARD_DATABASE_URL" -c '\d "user"' | grep two_factor_enabled

# 5. Bring up dashboard against $STAGING_DSN. Either:
#    - `bun run dev` (port 3001)  OR
#    - container build pointed at $STAGING_DSN
bun run build && bun run start &
sleep 5
STAGING_BASE=http://localhost:3001

# 6. END-TO-END FLOW SMOKE (manual OR via Playwright spec)
#    a. Sign up test admin (allowlist accepts):
curl -X POST "$STAGING_BASE/api/auth/sign-up/email" \
  -H "Content-Type: application/json" \
  -d '{"name":"Smoke Tester","email":"smoke@ifixtelecom.com.br","password":"SmokePass!2026"}'
#    → expect 200 + new user row.
#    b. Sign in:
curl -i -X POST "$STAGING_BASE/api/auth/sign-in/email" \
  -H "Content-Type: application/json" \
  -d '{"email":"smoke@ifixtelecom.com.br","password":"SmokePass!2026"}'
#    → Capture Set-Cookie. Set as PLAYWRIGHT_COOKIE_NO_2FA.
#    c. With that cookie, GET / → MUST redirect to /2fa/enroll (NO loop).
#    d. Complete enroll in browser: scan QR, type 6-digit code, save backup codes.
#    e. Logout → /signed-out.
#    f. Sign in again → middleware MUST redirect to /2fa/challenge.
#    g. Type TOTP code → reach dashboard home (302/307 → 200).
#    h. Run Playwright with PLAYWRIGHT_RUN_AUTHENTICATED_CASES=1 + the 3
#       cookie env vars:
PLAYWRIGHT_RUN_AUTHENTICATED_CASES=1 \
PLAYWRIGHT_COOKIE_NO_2FA="<from step b>" \
PLAYWRIGHT_COOKIE_ENROLLED="<post-enroll login Set-Cookie>" \
PLAYWRIGHT_COOKIE_VERIFIED="<post-TOTP-verify Set-Cookie>" \
DASHBOARD_BASE_URL="$STAGING_BASE" \
bunx playwright test tests/e2e/auth-redirect.spec.ts
#    → expect 4/4 passed.

# 7. Rate-limit spot check: 6 wrong passwords → 6th is 429.
for i in $(seq 1 6); do
  curl -s -o /dev/null -w "%{http_code}\n" \
    -X POST "$STAGING_BASE/api/auth/sign-in/email" \
    -H "Content-Type: application/json" \
    -d '{"email":"smoke@ifixtelecom.com.br","password":"Wrong!000"}'
done
#    → expect 401/400/400/400/400/429 (or similar — 6th MUST be 429).

# 8. Allowlist spot check.
curl -s -o /dev/null -w "%{http_code}\n" \
  -X POST "$STAGING_BASE/api/auth/sign-up/email" \
  -H "Content-Type: application/json" \
  -d '{"name":"X","email":"x@gmail.com","password":"AnyPass!2026"}'
#    → expect 400 (or 422) with allowlist error.

# 9. Backup-code spot check.
#    From /2fa/challenge click "Usar código de backup" → /2fa/backup → enter
#    one of the 10 saved codes → reach dashboard.

# 10. Cleanup temp env.
shred -u /tmp/dashboard-staging.env
```

## Outcomes (operator fills in)

| Step | Outcome | Notes (sanitized — NO DSN, NO password) |
|------|---------|-----------------------------------------|
| 0    | Option chosen: ? (1/2/3) | |
| 2    | Dry-run inspected: ? | Expected ALTER + CREATE statements |
| 3    | Real migrate ran: ? | |
| 4    | Schema verified: ? | twoFactor table + two_factor_enabled col |
| 6a   | Sign-up allowlist accept: ? | |
| 6c   | Middleware → /2fa/enroll (no loop): ? | |
| 6d   | Enroll complete: ? | |
| 6f   | Middleware → /2fa/challenge (no loop): ? | |
| 6g   | TOTP verify → dashboard: ? | |
| 6h   | Playwright 4/4: ? | |
| 7    | Rate-limit 429 on 6th: ? | |
| 8    | Allowlist 400 on non-Ifix: ? | |
| 9    | Backup-code path: ? | |
| 10   | Temp env destroyed: ? | |

## Abort criteria

DO NOT advance to Task 11-02-07 (prod migrate) if any of these tripped:
- Migrate dry-run shows unexpected DROP/ALTER on existing tables.
- End-to-end redirect-loop on /2fa/challenge or /2fa/enroll.
- twoFactorEnabled / twoFactorVerified not present in the session
  cookie (inspect via browser devtools → cookies →
  `better-auth.session_data`).
- Rate-limit returns 200 on the 6th attempt.
- Backup-code path fails.

If any abort criterion trips, report back to the orchestrator with the
failing step number; the executor will re-evaluate (typically Task
11-02-02 cookie-claim wiring needs adjustment — Option A → Option B
fallback, or rateLimit storage mis-configured).

## Resume signal (operator → orchestrator)

```
staging smoke PASS — option=<1|2|3> schema=<name>;
enroll→challenge no-loop; rate-limit 429 verified;
allowlist 400 verified; backup-code path verified
```

OR describe blocker with the failing step number.
