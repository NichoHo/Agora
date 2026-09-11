import { notFound, redirect } from "next/navigation";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import Reveal from "@/components/motion/Reveal";
import { fetchOrder, fetchWallet, marketPost, payDeposit, usd } from "@/lib/api";
import { getToken } from "@/lib/auth";

async function payAction(formData: FormData) {
  "use server";
  const token = await getToken();
  const orderID = String(formData.get("order_id"));
  if (!token) redirect("/auth/start");
  const resp = await marketPost(token, `/orders/${orderID}/pay`);
  if (resp.status === 402) redirect(`/checkout/${orderID}?error=funds`);
  if (!resp.ok) redirect(`/checkout/${orderID}?error=pay`);
  redirect(`/orders/${orderID}`);
}

async function topUpAction(formData: FormData) {
  "use server";
  const token = await getToken();
  const orderID = String(formData.get("order_id"));
  if (!token) redirect("/auth/start");
  await payDeposit(token, 50_000);
  redirect(`/checkout/${orderID}`);
}

async function cancelAction(formData: FormData) {
  "use server";
  const token = await getToken();
  const orderID = String(formData.get("order_id"));
  if (!token) redirect("/auth/start");
  await marketPost(token, `/orders/${orderID}/cancel`);
  redirect("/orders");
}

export default async function CheckoutPage({
  params,
  searchParams,
}: {
  params: Promise<{ orderId: string }>;
  searchParams: Promise<{ error?: string }>;
}) {
  const token = await getToken();
  const { orderId } = await params;
  if (!token) redirect(`/auth/start?next=/checkout/${orderId}`);
  const [order, wallet, sp] = await Promise.all([
    fetchOrder(token, orderId),
    fetchWallet(token),
    searchParams,
  ]);
  if (!order) notFound();
  if (order.status !== "pending_payment") redirect(`/orders/${order.id}`);
  const balance = wallet?.balance_minor ?? 0;
  const short = balance < order.price_minor;

  return (
    <Reveal mode="mount" className="mx-auto max-w-md">
      <h1 className="mb-4 text-xl font-bold tracking-tight text-ink">Checkout</h1>
      <Card>
        <CardContent className="flex items-center gap-3">
          <img
            src={order.listing_image || "https://picsum.photos/seed/vault/160/120"}
            alt=""
            className="h-16 w-20 rounded-control object-cover"
          />
          <div className="min-w-0">
            <p className="truncate text-sm font-medium text-ink">{order.listing_title}</p>
            <p className="money text-lg font-bold text-ink">{usd(order.price_minor)}</p>
          </div>
        </CardContent>
        <CardContent className="text-sm">
          <dl className="flex flex-col gap-1 border-t border-line pt-4">
            <div className="flex justify-between">
              <dt className="text-muted-foreground">Your wallet</dt>
              <dd className={`money ${short ? "text-danger" : "text-ink"}`}>{usd(balance)}</dd>
            </div>
            <div className="flex justify-between">
              <dt className="text-muted-foreground">Held in escrow until you confirm receipt</dt>
              <dd className="money text-ink">{usd(order.price_minor)}</dd>
            </div>
          </dl>
        </CardContent>
      </Card>

      {sp.error === "funds" || short ? (
        <div className="mt-4 rounded-control bg-danger-tint px-3 py-2 text-sm text-danger">
          Not enough funds in your wallet.
          <form action={topUpAction} className="mt-2">
            <input type="hidden" name="order_id" value={order.id} />
            <Button type="submit" size="sm">
              Add $500 demo funds
            </Button>
          </form>
        </div>
      ) : null}
      {sp.error === "pay" ? (
        <p className="mt-4 rounded-control bg-danger-tint px-3 py-2 text-sm text-danger">
          Payment failed. Try again.
        </p>
      ) : null}

      <form action={payAction} className="mt-4">
        <input type="hidden" name="order_id" value={order.id} />
        <Button type="submit" disabled={short} className="w-full">
          Pay {usd(order.price_minor)}
        </Button>
      </form>
      <form action={cancelAction} className="mt-2">
        <input type="hidden" name="order_id" value={order.id} />
        <Button type="submit" variant="outline" className="w-full">
          Cancel order
        </Button>
      </form>
      <p className="mt-3 text-center text-xs text-faint">
        The reservation holds this item for 15 minutes.
      </p>
    </Reveal>
  );
}
