import { cookies } from "next/headers";
import { redirect } from "next/navigation";
import { ID_URL, WEB_ORIGIN, SECURE_COOKIES } from "@/lib/env";
import { safePath } from "@/lib/auth";

// OIDC redirect_uri: exchanges the auth code (server-side, with the PKCE
// verifier) and stores the access token in an HttpOnly cookie.
export async function GET(req: Request) {
  const url = new URL(req.url);
  const code = url.searchParams.get("code") ?? "";
  const state = url.searchParams.get("state") ?? "";

  const c = await cookies();
  const expectedState = c.get("oauth_state")?.value;
  const verifier = c.get("pkce_verifier")?.value ?? "";
  const next = safePath(c.get("post_auth_next")?.value);
  c.delete("oauth_state");
  c.delete("pkce_verifier");
  c.delete("post_auth_next");

  if (!code || !state || !expectedState || state !== expectedState) {
    redirect("/?auth_error=state");
  }

  const resp = await fetch(`${ID_URL}/token`, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      grant_type: "authorization_code",
      code,
      redirect_uri: `${WEB_ORIGIN}/auth/callback`,
      client_id: "vault-web",
      code_verifier: verifier,
    }),
    cache: "no-store",
  });
  if (!resp.ok) {
    redirect("/?auth_error=token");
  }
  const tok = (await resp.json()) as {
    access_token: string;
    refresh_token?: string;
    expires_in: number;
  };
  c.set("vault_token", tok.access_token, {
    httpOnly: true,
    sameSite: "lax",
    secure: SECURE_COOKIES,
    path: "/",
    maxAge: tok.expires_in,
  });
  if (tok.refresh_token) {
    c.set("vault_refresh", tok.refresh_token, {
      httpOnly: true,
      sameSite: "lax",
      secure: SECURE_COOKIES,
      path: "/",
      maxAge: 30 * 24 * 3600,
    });
  }
  redirect(next);
}
