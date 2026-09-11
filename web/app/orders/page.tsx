import Link from "next/link";
import { redirect } from "next/navigation";
import StatusBadge from "@/components/StatusBadge";
import ServiceUnavailable from "@/components/ServiceUnavailable";
import Reveal from "@/components/motion/Reveal";
import StaggerGrid from "@/components/motion/StaggerGrid";
import { fetchOrders, usd, type Order } from "@/lib/api";
import { getToken } from "@/lib/auth";

export default async function OrdersPage({
  searchParams,
}: {
  searchParams: Promise<{ role?: string }>;
}) {
  const token = await getToken();
  if (!token) redirect("/auth/start?next=/orders");
  const sp = await searchParams;
  const role = sp.role === "seller" ? "seller" : "buyer";
  // "no orders" and "orders service is down" must not look identical
  let orders: Order[] = [];
  let unreachable = false;
  try {
    orders = await fetchOrders(token, role);
  } catch {
    unreachable = true;
  }

  const tab = (r: string, label: string) => (
    <Link
      href={`/orders?role=${r}`}
      className={`rounded-control px-3 py-1.5 text-sm font-medium transition-colors ${
        role === r
          ? "bg-primary text-on-solid"
          : "border border-line text-muted-foreground hover:bg-fill hover:text-ink"
      }`}
    >
      {label}
    </Link>
  );

  return (
    <Reveal mode="mount" className="mx-auto max-w-2xl">
      <h1 className="mb-4 text-xl font-bold tracking-tight text-ink">Orders</h1>
      <div className="mb-4 flex gap-2">
        {tab("buyer", "Purchases")}
        {tab("seller", "Sales")}
      </div>
      {unreachable ? (
        <ServiceUnavailable
          service="orders"
          detail="Your orders can’t be loaded right now. It doesn’t mean you have none."
        />
      ) : orders.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          Nothing here yet. {role === "buyer" ? "Go buy something nice." : "List something for sale."}
        </p>
      ) : (
        <StaggerGrid className="flex flex-col gap-2">
          {orders.map((o) => (
            <Link
              key={o.id}
              href={`/orders/${o.id}`}
              className="flex items-center gap-3 rounded-card border border-line bg-surface p-3 transition-colors hover:border-line-strong"
            >
              <img
                src={o.listing_image || "https://picsum.photos/seed/vault/160/120"}
                alt=""
                className="h-14 w-18 rounded-control object-cover"
              />
              <div className="min-w-0 flex-1">
                <p className="truncate text-sm font-medium text-ink">{o.listing_title}</p>
                <p className="text-xs text-faint">
                  {new Date(o.created_at).toLocaleDateString()}
                </p>
              </div>
              <span className="money text-sm font-bold text-ink">{usd(o.price_minor)}</span>
              <StatusBadge status={o.status} />
            </Link>
          ))}
        </StaggerGrid>
      )}
    </Reveal>
  );
}
