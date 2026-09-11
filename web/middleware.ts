import { NextRequest, NextResponse } from "next/server";
import { SECURE_COOKIES } from "@/lib/env";

// Rotating-refresh middleware: when the access token is missing/expired but a
// refresh token exists, rotate it at the IdP and continue the request with
// fresh cookies. Reuse of a stale refresh token is detected server-side by the
// IdP and revokes the whole token family.

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

export async function middleware(req: NextRequest) {
  const access = req.cookies.get("vault_token")?.value;
  const refresh = req.cookies.get("vault_refresh")?.value;
  if (!refresh || (access && !expired(access))) return NextResponse.next();

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
      return out;
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
    return out;
  } catch {
    return NextResponse.next();
  }
}

export const config = {
  // pages only: skip static assets and the auth endpoints themselves
  matcher: ["/((?!_next|favicon|auth/|idp/).*)"],
};
