import { cookies } from "next/headers";

export type User = { sub: string; email: string };

export async function getToken(): Promise<string | null> {
  const c = await cookies();
  return c.get("agora_token")?.value ?? null;
}

// Decodes the access token payload for UI purposes only, with no signature check
// here. The market service verifies signatures against the IdP's JWKS; the
// storefront never trusts this value for anything but display.
export async function getUser(): Promise<User | null> {
  const token = await getToken();
  if (!token) return null;
  const parts = token.split(".");
  if (parts.length !== 3) return null;
  try {
    const payload = JSON.parse(Buffer.from(parts[1], "base64url").toString());
    if (typeof payload.exp !== "number" || payload.exp * 1000 < Date.now()) return null;
    return { sub: payload.sub, email: payload.email ?? "" };
  } catch {
    return null;
  }
}

// Only allow same-site relative paths as post-auth destinations.
export function safePath(p: string | undefined | null): string {
  if (!p || !p.startsWith("/") || p.startsWith("//")) return "/";
  return p;
}
