import { NextRequest, NextResponse } from "next/server";
import { SECURE_COOKIES } from "@/lib/env";

// Rotating-refresh middleware: when the access token is missing/expired but a
// refresh token exists, rotate it at the IdP and continue the request with
// fresh cookies. Reuse of a stale refresh token is detected server-side by the
// IdP and revokes the whole token family.
//
// This also carries the CSP: Next.js's App Router injects its own inline
// hydration/RSC-payload scripts into every page, which a static
// `script-src 'self'` (the first version of this policy, set as a plain
// header in next.config.ts) blocks outright — every client component on the
// site silently failed to hydrate, so every onClick/onSubmit handler was
// dead and forms fell back to a raw browser GET. Caught by hand-testing
// /auth/forgot in a live browser, not by any test, since a broken CSP
// doesn't fail a build or a `fetch`-based test the way a broken handler
// would. Per Next's own documented pattern, the fix is a per-request nonce
// generated here (middleware runs before rendering, so the nonce exists
// before Next stamps its own scripts with it) plus `strict-dynamic`, so
// framework-injected scripts still work without allowlisting each one.
const isDev = process.env.NODE_ENV === "development";

function cspHeader(nonce: string): string {
  return `
    default-src 'self';
    script-src 'self' 'nonce-${nonce}' 'strict-dynamic'${isDev ? " 'unsafe-eval'" : ""};
    style-src 'self' 'unsafe-inline';
    img-src 'self' https: data:;
    font-src 'self';
    connect-src 'self';
    object-src 'none';
    base-uri 'self';
    form-action 'self';
    frame-ancestors 'none';
    upgrade-insecure-requests;
  `
    .replace(/\s{2,}/g, " ")
    .trim();
}

function expired(token: string): boolean {
  const parts = token.split(".");
  if (parts.length !== 3) return true;
  try {
    const payload = JSON.parse(Buffer.from(parts[1], "base64url").toString());
    // refresh 60s early so in-flight requests don't race expiry
    return typeof payload.exp !== "number" || payload.exp * 1000 < Date.now() + 60_000;
  } catch {
    return true;
  }
}

async function withCSP(req: NextRequest, res: NextResponse): Promise<NextResponse> {
  const nonce = Buffer.from(crypto.randomUUID()).toString("base64");
  const policy = cspHeader(nonce);
  // request header: lets Next's renderer (which runs after middleware, using
  // this request) stamp the same nonce onto the scripts it injects for this
  // request. Response header: what the browser actually enforces against.
  res.headers.set("x-nonce", nonce);
  res.headers.set("Content-Security-Policy", policy);
  return res;
}

export async function middleware(req: NextRequest) {
  // /auth/* and /idp/* skip token rotation (the auth flow manages its own
  // cookies; /idp/* is a proxied API response, not a rendered page), but
  // every path still needs the CSP, including these — this is exactly the
  // set of pages the bug above broke.
  if (/^\/(auth\/|idp\/)/.test(req.nextUrl.pathname)) {
    return withCSP(req, NextResponse.next());
  }

  const access = req.cookies.get("vault_token")?.value;
  const refresh = req.cookies.get("vault_refresh")?.value;
  if (!refresh || (access && !expired(access))) return withCSP(req, NextResponse.next());

  const idURL = process.env.ID_URL ?? "http://localhost:8081";
  try {
    const resp = await fetch(`${idURL}/token`, {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: new URLSearchParams({
        grant_type: "refresh_token",
        refresh_token: refresh,
        client_id: "vault-web",
      }),
      cache: "no-store",
    });
    if (!resp.ok) {
      // rotated-away or revoked family: drop both cookies, act signed out
      const out = NextResponse.next();
      out.cookies.delete("vault_token");
      out.cookies.delete("vault_refresh");
      return withCSP(req, out);
    }
    const tok = (await resp.json()) as {
      access_token: string;
      refresh_token: string;
      expires_in: number;
    };
    // make the fresh token visible to this request's server components too
    req.cookies.set("vault_token", tok.access_token);
    const out = NextResponse.next({ request: { headers: req.headers } });
    out.cookies.set("vault_token", tok.access_token, {
      httpOnly: true, sameSite: "lax", secure: SECURE_COOKIES, path: "/", maxAge: tok.expires_in,
    });
    out.cookies.set("vault_refresh", tok.refresh_token, {
      httpOnly: true, sameSite: "lax", secure: SECURE_COOKIES, path: "/", maxAge: 30 * 24 * 3600,
    });
    return withCSP(req, out);
  } catch {
    return withCSP(req, NextResponse.next());
  }
}

export const config = {
  // every page needs the CSP; only static assets are skipped
  matcher: ["/((?!_next|favicon).*)"],
};
