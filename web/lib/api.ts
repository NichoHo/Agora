import { MARKET_URL, PAY_URL } from "./env";

export type Listing = {
  id: string;
  seller_id: string;
  category_id: number | null;
  title: string;
  description: string;
  price_minor: number;
  currency: string;
  status: string;
  image_url: string;
  created_at: string;
};

export type Category = { id: number; name: string; slug: string };

const usdFormat = new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" });

// minor units are cents; negatives render as "-$12.34"
export function usd(amountMinor: number): string {
  return usdFormat.format(amountMinor / 100);
}

export async function fetchListings(params: {
  q?: string;
  category?: string;
  limit?: number;
}): Promise<{ items: Listing[]; next_cursor: string }> {
  const q = new URLSearchParams();
  if (params.q) q.set("q", params.q);
  if (params.category) q.set("category", params.category);
  if (params.limit) q.set("limit", String(params.limit));
  const resp = await fetch(`${MARKET_URL}/listings?${q}`, { cache: "no-store" });
  if (!resp.ok) throw new Error(`listings: ${resp.status}`);
  return resp.json();
}

export async function fetchListing(id: string): Promise<Listing | null> {
  const resp = await fetch(`${MARKET_URL}/listings/${id}`, { cache: "no-store" });
  if (!resp.ok) return null;
  return resp.json();
}

export async function fetchCategories(): Promise<Category[]> {
  // try/catch as well as the !ok check: a refused connection rejects the fetch
  // itself, which would otherwise 500 every page that renders the category row.
  try {
    const resp = await fetch(`${MARKET_URL}/categories`, { cache: "no-store" });
    if (!resp.ok) return [];
    return resp.json();
  } catch {
    return []; // market service unreachable
  }
}

export type Order = {
  id: string;
  listing_id: string;
  buyer_id: string;
  seller_id: string;
  price_minor: number;
  status: string;
  created_at: string;
  funded_at: string | null;
  shipped_at: string | null;
  completed_at: string | null;
  listing_title: string;
  listing_image: string;
};

export type WalletEntry = {
  amount_minor: number;
  kind: string;
  reference: string;
  created_at: string;
};

export type Wallet = { balance_minor: number; entries: WalletEntry[] };

function bearer(token: string): HeadersInit {
  return { Authorization: `Bearer ${token}`, "Content-Type": "application/json" };
}

export async function marketPost(
  token: string,
  path: string,
  body?: unknown,
): Promise<Response> {
  return fetch(`${MARKET_URL}${path}`, {
    method: "POST",
    headers: bearer(token),
    body: body === undefined ? undefined : JSON.stringify(body),
    cache: "no-store",
  });
}

export async function fetchOrder(token: string, id: string): Promise<Order | null> {
  const resp = await fetch(`${MARKET_URL}/orders/${id}`, {
    headers: bearer(token),
    cache: "no-store",
  });
  if (!resp.ok) return null;
  return resp.json();
}

export async function fetchOrders(token: string, role: "buyer" | "seller"): Promise<Order[]> {
  const resp = await fetch(`${MARKET_URL}/orders?role=${role}`, {
    headers: bearer(token),
    cache: "no-store",
  });
  if (!resp.ok) return [];
  return resp.json();
}

export async function fetchWallet(token: string): Promise<Wallet | null> {
  const resp = await fetch(`${PAY_URL}/wallet`, { headers: bearer(token), cache: "no-store" });
  if (!resp.ok) return null;
  return resp.json();
}

export async function payDeposit(token: string, amountMinor: number): Promise<boolean> {
  // key granularity of one minute makes accidental double-clicks idempotent
  const key = `topup:${new Date().toISOString().slice(0, 16)}`;
  const resp = await fetch(`${PAY_URL}/deposits`, {
    method: "POST",
    headers: bearer(token),
    body: JSON.stringify({ idempotency_key: key, amount_minor: amountMinor }),
    cache: "no-store",
  });
  return resp.ok;
}
