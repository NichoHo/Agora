"use server";

import { redirect } from "next/navigation";
import { getToken } from "@/lib/auth";
import { ASSIST_URL, MARKET_URL } from "@/lib/env";

export type Suggestion = {
  suggestion_id: string;
  title: string;
  description: string;
  category_slug: string;
  price_low: number | null;
  price_high: number | null;
  model: string;
};

export async function suggestAction(
  imageUrl: string,
  titleHint: string,
): Promise<Suggestion | { error: string }> {
  const token = await getToken();
  if (!token) return { error: "signed_out" };
  try {
    const resp = await fetch(`${ASSIST_URL}/suggest`, {
      method: "POST",
      headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
      body: JSON.stringify({ image_url: imageUrl, title_hint: titleHint }),
      cache: "no-store",
    });
    if (!resp.ok) return { error: `assist_${resp.status}` };
    return (await resp.json()) as Suggestion;
  } catch {
    return { error: "assist_unreachable" };
  }
}

export async function createListingAction(formData: FormData) {
  // server actions are API routes: re-authenticate here, not just in the page
  const token = await getToken();
  if (!token) redirect("/auth/start?next=/sell");

  const price = Number(formData.get("price"));
  const categoryId = Number(formData.get("category_id"));
  const resp = await fetch(`${MARKET_URL}/listings`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: JSON.stringify({
      title: String(formData.get("title") ?? ""),
      description: String(formData.get("description") ?? ""),
      price_minor: Math.round(price * 100), // dollars → cents
      category_id: categoryId > 0 ? categoryId : null,
      image_url: String(formData.get("image_url") ?? ""),
    }),
    cache: "no-store",
  });
  if (!resp.ok) redirect("/sell?error=1");
  const listing = (await resp.json()) as { id: string };

  // co-creation metric: report which suggested fields survived unedited.
  // awaited, because redirect() aborts pending work and a fire-and-forget call is lost
  const suggestionID = String(formData.get("suggestion_id") ?? "");
  const accepted = String(formData.get("accepted_fields") ?? "");
  if (suggestionID) {
    await fetch(`${ASSIST_URL}/suggestions/${suggestionID}/outcome`, {
      method: "POST",
      headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
      body: JSON.stringify({
        accepted_fields: accepted ? accepted.split(",") : [],
        listing_id: listing.id,
      }),
      cache: "no-store",
    }).catch(() => {});
  }
  redirect(`/listing/${listing.id}`);
}
