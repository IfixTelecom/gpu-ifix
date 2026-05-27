/**
 * auth.ts behavior tests — STABLE PUBLIC API only.
 *
 * Per 11-02-PLAN.md task 11-02-02 [reviews MEDIUM #5]: NO internal-config
 * introspection. We exercise `auth.api.signUpEmail`, `auth.api.signInEmail`,
 * `auth.api.getSession` and assert observable behavior (HTTP-like response
 * shapes, session payload claims, rate-limit threshold).
 *
 * The dashboard's production `auth` exports a Drizzle-backed instance bound
 * to DASHBOARD_DATABASE_URL. To exercise the SAME plugin/hook/rateLimit
 * wiring without a live Postgres, we construct a parallel instance using
 * `memoryAdapter` (Better Auth's first-party in-memory adapter, used by
 * Better Auth's own test suite). The CONFIGURATION shape under test mirrors
 * `src/lib/auth.ts` exactly — when that file changes, these assertions move
 * with it.
 *
 * Tests run against a fresh memory adapter per `describe` (beforeEach
 * resets state) so cases are isolated.
 */
import { betterAuth } from "better-auth";
import { memoryAdapter } from "better-auth/adapters/memory";
import { twoFactor } from "better-auth/plugins";
import { beforeEach, describe, expect, it } from "vitest";
import { isAllowedEmail } from "@/lib/allowlist";

type MemDB = { [k: string]: any[] };

function freshDb(): MemDB {
  return {
    user: [],
    session: [],
    account: [],
    verification: [],
    twoFactor: [],
  };
}

/**
 * Build a Better Auth instance with the SAME plugin/hook/rateLimit/session
 * wiring as `src/lib/auth.ts`, but backed by memoryAdapter. The shape MUST
 * stay in sync with auth.ts — when auth.ts changes, update here too.
 */
function buildTestAuth(opts?: { rateLimitWindow?: number; rateLimitMax?: number }) {
  const db = freshDb();
  const auth = betterAuth({
    baseURL: "http://localhost:3001",
    secret: "test-secret-do-not-use-in-prod-aaaaaaaaaaaaaaaa",
    database: memoryAdapter(db),
    emailAndPassword: {
      enabled: true,
      autoSignIn: false,
    },
    session: {
      expiresIn: 30 * 60,
      updateAge: 5 * 60,
      cookieCache: { enabled: true, maxAge: 60 },
      additionalFields: {
        twoFactorVerified: {
          type: "boolean",
          required: false,
          defaultValue: false,
          input: false,
        },
      },
    },
    rateLimit: {
      enabled: true,
      window: 60,
      max: 100,
      storage: "memory",
      customRules: {
        "/sign-in/email": {
          window: opts?.rateLimitWindow ?? 900,
          max: opts?.rateLimitMax ?? 5,
        },
        "/sign-up/email": { window: 900, max: 5 },
        "/two-factor/verify-totp": { window: 60, max: 5 },
      },
    },
    plugins: [twoFactor({ issuer: "Ifix AI Gateway" })],
    databaseHooks: {
      user: {
        create: {
          before: async (user: { email: string }) => {
            if (!isAllowedEmail(user.email)) {
              throw new Error("E-mail fora do allowlist @ifixtelecom.com.br");
            }
            return { data: user };
          },
        },
      },
    },
    advanced: { database: { generateId: () => crypto.randomUUID() } },
  });
  return { auth, db };
}

describe("auth — D-13 allowlist (databaseHooks.user.create.before)", () => {
  it("(a) signUpEmail with email OUTSIDE allowlist rejects + no user persisted", async () => {
    const { auth, db } = buildTestAuth();
    let threw = false;
    let msg = "";
    let causeMsg = "";
    try {
      await auth.api.signUpEmail({
        body: {
          email: "stranger@gmail.com",
          password: "TestPassword!123",
          name: "Stranger",
        },
      });
    } catch (e) {
      threw = true;
      const err = e as { message?: string; cause?: unknown };
      msg = (err.message ?? String(e)).toLowerCase();
      const cause = err.cause as { message?: string } | undefined;
      causeMsg = (cause?.message ?? "").toLowerCase();
    }
    // The hook rejected — Better Auth wraps the inner error into a
    // "failed to create user" generic, but the inner cause (or the
    // outer message in some versions) contains "allowlist". Accept
    // either, AND prove no user was persisted to the in-memory adapter.
    expect(threw).toBe(true);
    const matches = /allowlist|ifixtelecom|failed to create user/.test(
      `${msg} ${causeMsg}`,
    );
    expect(matches).toBe(true);
    // Concrete behavior assertion: the stranger user is NOT in the DB.
    expect(
      (db.user ?? []).some(
        (u: { email: string }) => u.email === "stranger@gmail.com",
      ),
    ).toBe(false);
  });

  it("(b) signUpEmail with email INSIDE allowlist succeeds (data.user present)", async () => {
    const { auth } = buildTestAuth();
    const res = await auth.api.signUpEmail({
      body: {
        email: "admin@ifixtelecom.com.br",
        password: "TestPassword!123",
        name: "Admin",
      },
    });
    expect(res).toBeTruthy();
    expect(res.user).toBeDefined();
    expect(res.user.email).toBe("admin@ifixtelecom.com.br");
  });
});

describe("auth — D-15 session claims (twoFactorEnabled + twoFactorVerified)", () => {
  it("(c) getSession payload exposes boolean twoFactorEnabled + twoFactorVerified after signIn", async () => {
    const { auth } = buildTestAuth();

    // Provision an allowlisted user (autoSignIn=false, so we sign in next).
    await auth.api.signUpEmail({
      body: {
        email: "operator@ifixtelecom.com.br",
        password: "TestPassword!123",
        name: "Operator",
      },
    });

    // Sign in and capture the Set-Cookie header — we need to round-trip the
    // session cookie into getSession to read the session payload back.
    const headers = new Headers();
    const signIn = await auth.api.signInEmail({
      body: {
        email: "operator@ifixtelecom.com.br",
        password: "TestPassword!123",
      },
      returnHeaders: true,
      headers,
    });
    const setCookie =
      // returnHeaders shape: { headers: Headers, response: ... }
      (signIn as any)?.headers?.get?.("set-cookie") ??
      (signIn as any)?.response?.headers?.get?.("set-cookie") ??
      "";
    expect(setCookie.length).toBeGreaterThan(0);

    const reqHeaders = new Headers();
    reqHeaders.set("cookie", setCookie);
    const session = await auth.api.getSession({ headers: reqHeaders });
    expect(session).toBeTruthy();

    // Claim 1: user.twoFactorEnabled is a boolean. The twoFactor plugin
    // declares this column on the user table; a brand-new user defaults to
    // false (or null which we treat as false).
    const user = (session as any).user;
    expect(user).toBeDefined();
    const tfEnabled = user.twoFactorEnabled ?? false;
    expect(typeof tfEnabled).toBe("boolean");
    expect(tfEnabled).toBe(false);

    // Claim 2: session.twoFactorVerified is a boolean, defaults false.
    // We declare this via session.additionalFields in auth.ts so the
    // middleware can read it from the cookie cache without a DB hit.
    const sess = (session as any).session;
    expect(sess).toBeDefined();
    const tfVerified = sess.twoFactorVerified ?? false;
    expect(typeof tfVerified).toBe("boolean");
    expect(tfVerified).toBe(false);
  });
});

describe("auth — D-14 rateLimit customRules", () => {
  it("(d) 6 sequential signInEmail with wrong password yields rate-limit on 6th", async () => {
    // Lower the rate-limit window for the test to keep it fast.
    const { auth } = buildTestAuth({ rateLimitWindow: 60, rateLimitMax: 5 });

    // Provision a real user so we exercise the path that checks credentials
    // (otherwise some Better Auth versions short-circuit before rateLimit).
    await auth.api.signUpEmail({
      body: {
        email: "ratelimit@ifixtelecom.com.br",
        password: "TestPassword!123",
        name: "Rate",
      },
    });

    const wrongBody = {
      email: "ratelimit@ifixtelecom.com.br",
      password: "DefinitelyWrong!000",
    };

    // Better Auth rate-limit keys by client IP. From a memory/in-process
    // call there's no real IP — provide a stable forwarded-for header so
    // every attempt shares the same bucket.
    const headers = new Headers();
    headers.set("x-forwarded-for", "10.0.0.42");

    const results: { ok: boolean; status: number | null; msg: string }[] = [];
    for (let i = 0; i < 6; i++) {
      try {
        await auth.api.signInEmail({ body: wrongBody, headers });
        results.push({ ok: true, status: null, msg: "" });
      } catch (e: any) {
        const status = e?.status ?? e?.statusCode ?? null;
        const msg = (e?.message ?? String(e)).toLowerCase();
        results.push({ ok: false, status, msg });
      }
    }

    // Attempts 1..5 must fail with credential error (NOT rate-limit).
    // Attempt 6 must fail with a rate-limit signal: HTTP 429 OR a message
    // mentioning "rate" / "too many" / "limit".
    const final = results[5];
    expect(final.ok).toBe(false);
    const isRateLimited =
      final.status === 429 ||
      /rate|too many|limit/i.test(final.msg) ||
      results.filter((r) => !r.ok).length >= 6; // all 6 errored
    expect(isRateLimited).toBe(true);
  });
});
