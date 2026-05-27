/**
 * Auth boundary — every dashboard route except /login, /signup, /2fa/*,
 * /first-login, /signed-out, /api/auth/* is gated. Phase 11 (PRD-06)
 * extends the original session-presence check with a two-stage TOTP gate
 * (D-12 + D-15) per 11-RESEARCH §Pitfall 4.
 *
 * Decision tree (reviews HIGH #2 — cookie-claim contract):
 *   1. No session cookie → redirect to /login?session_expired=1
 *   2. Session present but `user.twoFactorEnabled !== true` → /2fa/enroll
 *   3. Session present, twoFactorEnabled=true, `session.twoFactorVerified !== true` → /2fa/challenge
 *   4. Both claims present → next()
 *
 * Claims are read from the session COOKIE CACHE (Better Auth's
 * `getCookieCache`, configured in `lib/auth.ts` with `cookieCache.enabled`
 * + `session.additionalFields` exposing `twoFactorVerified` AND the
 * twoFactor plugin contributing `user.twoFactorEnabled`). The Edge
 * runtime MUST NOT make a DB call — this is the contract Task 11-02-02
 * wires.
 *
 * If `getCookieCache` returns null (cache stale or absent — e.g. just
 * after sign-in before the first cookieCache write), we conservatively
 * treat as session-present-but-unverified and route to /2fa/challenge.
 * This is safe: the challenge page itself runs through this matcher
 * exclusion (see `config.matcher` below) so we cannot loop.
 *
 * Matcher exclusions (UI-SPEC v2 §Anchors): login, signup, 2fa, first-login,
 * signed-out, api/auth, _next, favicon.
 */
import { getCookieCache, getSessionCookie } from "better-auth/cookies";
import { type NextRequest, NextResponse } from "next/server";

/**
 * Read twoFactor claims from the Better Auth session cookie cache.
 * Returns:
 *   - { hasSession: false } when no session cookie present.
 *   - { hasSession: true, twoFactorEnabled, twoFactorVerified } when the
 *     cookieCache payload was successfully decoded.
 *   - { hasSession: true, twoFactorEnabled: false, twoFactorVerified: false }
 *     when the session cookie exists but cookieCache is missing/stale —
 *     we conservatively route through enroll/challenge gates.
 */
async function readTwoFactorClaims(req: NextRequest): Promise<{
  hasSession: boolean;
  twoFactorEnabled: boolean;
  twoFactorVerified: boolean;
}> {
  const sessionCookie = getSessionCookie(req);
  if (!sessionCookie) {
    return {
      hasSession: false,
      twoFactorEnabled: false,
      twoFactorVerified: false,
    };
  }

  const cache = await getCookieCache(req);
  if (!cache || !cache.session || !cache.user) {
    // Cookie cache stale/absent — treat as session-present-but-unverified.
    // Routes to enroll first (the more conservative gate).
    return {
      hasSession: true,
      twoFactorEnabled: false,
      twoFactorVerified: false,
    };
  }

  const user = cache.user as { twoFactorEnabled?: boolean };
  const session = (cache.session as { twoFactorVerified?: boolean }) ?? {};
  return {
    hasSession: true,
    twoFactorEnabled: user.twoFactorEnabled === true,
    twoFactorVerified: session.twoFactorVerified === true,
  };
}

export async function middleware(req: NextRequest) {
  const claims = await readTwoFactorClaims(req);

  // Stage 1: no session → /login?session_expired=1 unless already
  // somewhere outside the matcher.
  if (!claims.hasSession) {
    const url = new URL("/login", req.url);
    url.searchParams.set("session_expired", "1");
    return NextResponse.redirect(url);
  }

  // Stage 2a: session present, 2FA not enrolled → /2fa/enroll.
  if (!claims.twoFactorEnabled) {
    return NextResponse.redirect(new URL("/2fa/enroll", req.url));
  }

  // Stage 2b: session present, enrolled but this session hasn't verified
  // TOTP yet → /2fa/challenge.
  if (!claims.twoFactorVerified) {
    return NextResponse.redirect(new URL("/2fa/challenge", req.url));
  }

  return NextResponse.next();
}

// Matcher excludes the auth-flow routes so the middleware does NOT
// redirect-loop on /2fa, /first-login, /signed-out, /login, /signup, or
// the Better Auth API itself.
export const config = {
  matcher: [
    "/((?!login|signup|2fa|first-login|signed-out|api/auth|_next|favicon).*)",
  ],
};
