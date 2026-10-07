import { cookies } from "next/headers";
import { redirect } from "next/navigation";
import { ID_URL } from "@/lib/env";

// Clears the storefront tokens AND the IdP session, so switching accounts in
// one browser actually shows the login screen again.
export async function GET() {
  const c = await cookies();
  const sid = c.get("agora_sid")?.value;
  if (sid) {
    await fetch(`${ID_URL}/logout`, {
      method: "POST",
      headers: { Cookie: `agora_sid=${sid}` },
      cache: "no-store",
    }).catch(() => {});
  }
  c.delete("agora_sid");
  c.delete("agora_token");
  c.delete("agora_refresh");
  redirect("/");
}
