import { createHash, randomBytes } from "crypto";
import { cookies } from "next/headers";
import { redirect } from "next/navigation";
import { WEB_ORIGIN, SECURE_COOKIES } from "@/lib/env";
import { safePath } from "@/lib/auth";

// Begins the OIDC Authorization Code + PKCE flow against our own IdP.
export async function GET(req: Request) {
  const url = new URL(req.url);
  const verifier = randomBytes(32).toString("base64url");
  const challenge = createHash("sha256").update(verifier).digest("base64url");
  const state = randomBytes(16).toString("base64url");

  const c = await cookies();
  const opts = {
    httpOnly: true,
    sameSite: "lax" as const,
    secure: SECURE_COOKIES,
    path: "/",
    maxAge: 600,
  };
  c.set("pkce_verifier", verifier, opts);
  c.set("oauth_state", state, opts);
  c.set("post_auth_next", safePath(url.searchParams.get("next")), opts);

  const q = new URLSearchParams({
    response_type: "code",
    client_id: "vault-web",
    redirect_uri: `${WEB_ORIGIN}/auth/callback`,
    scope: "openid profile",
    state,
    code_challenge: challenge,
    code_challenge_method: "S256",
    nonce: randomBytes(16).toString("base64url"),
  });
  redirect(`/idp/authorize?${q}`);
}
