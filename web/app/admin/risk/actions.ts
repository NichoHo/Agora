"use server";

import { revalidatePath } from "next/cache";
import { getToken } from "@/lib/auth";
import { RISK_URL } from "@/lib/env";

export async function resolveScoreAction(formData: FormData) {
  const token = await getToken();
  if (!token) return;
  await fetch(`${RISK_URL}/admin/scores/${Number(formData.get("id"))}/resolve`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: JSON.stringify({ action: String(formData.get("action")) }),
    cache: "no-store",
  }).catch(() => {});
  revalidatePath("/admin/risk");
}
