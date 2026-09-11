import Link from "next/link";
import { notFound, redirect } from "next/navigation";
import { Button, buttonVariants } from "@/components/ui/button";
import OrderTimeline from "@/components/OrderTimeline";
import StatusBadge from "@/components/StatusBadge";
import Reveal from "@/components/motion/Reveal";
import { fetchOrder, marketPost, usd } from "@/lib/api";
import { getToken, getUser } from "@/lib/auth";

async function runOrderAction(formData: FormData) {
  "use server";
  const token = await getToken();
  const orderID = String(formData.get("order_id"));
  const action = String(formData.get("action"));
  if (!token) redirect("/auth/start");
  if (["ship", "confirm", "cancel"].includes(action)) {
    await marketPost(token, `/orders/${orderID}/${action}`);
  }
  redirect(`/orders/${orderID}`);
}

function ActionButton({
  action,
  orderID,
  label,
  primary,
}: {
  action: string;
  orderID: string;
  label: string;
  primary?: boolean;
}) {
  return (
    <form action={runOrderAction}>
      <input type="hidden" name="order_id" value={orderID} />
      <input type="hidden" name="action" value={action} />
      <Button type="submit" variant={primary ? "default" : "outline"} className="w-full">
        {label}
      </Button>
    </form>
  );
}

export default async function OrderPage({ params }: { params: Promise<{ id: string }> }) {
  const token = await getToken();
  const { id } = await params;
  if (!token) redirect(`/auth/start?next=/orders/${id}`);
  const [order, user] = await Promise.all([fetchOrder(token, id), getUser()]);
  if (!order || !user) notFound();
  const isBuyer = user.sub === order.buyer_id;

  return (
    <Reveal mode="mount" className="mx-auto max-w-md">
      <div className="mb-4 flex items-center justify-between">
        <h1 className="text-xl font-bold tracking-tight text-ink">Order</h1>
        <StatusBadge status={order.status} />
      </div>
      <Link
        href={`/listing/${order.listing_id}`}
        className="flex items-center gap-3 rounded-card border border-line bg-surface p-3 transition-colors hover:border-line-strong"
      >
        <img
          src={order.listing_image || "https://picsum.photos/seed/vault/160/120"}
          alt=""
          className="h-16 w-20 rounded-control object-cover"
        />
        <div className="min-w-0">
          <p className="truncate text-sm font-medium text-ink">{order.listing_title}</p>
          <p className="money text-lg font-bold text-ink">{usd(order.price_minor)}</p>
          <p className="text-xs text-faint">You are the {isBuyer ? "buyer" : "seller"}</p>
        </div>
      </Link>

      <div className="mt-6">
        <OrderTimeline order={order} />
      </div>

      <div className="mt-4 flex flex-col gap-2">
        {isBuyer && order.status === "pending_payment" ? (
          <Link href={`/checkout/${order.id}`} className={buttonVariants({ className: "w-full" })}>
            Go to checkout
          </Link>
        ) : null}
        {!isBuyer && order.status === "funded" ? (
          <ActionButton action="ship" orderID={order.id} label="Mark as shipped" primary />
        ) : null}
        {isBuyer && order.status === "shipped" ? (
          <ActionButton
            action="confirm"
            orderID={order.id}
            label="Confirm it arrived and pay the seller"
            primary
          />
        ) : null}
        {order.status === "pending_payment" ? (
          <ActionButton action="cancel" orderID={order.id} label="Cancel order" />
        ) : null}
        {!isBuyer && order.status === "funded" ? (
          <ActionButton action="cancel" orderID={order.id} label="Cancel & refund buyer" />
        ) : null}
      </div>
      {isBuyer && order.status === "shipped" ? (
        <p className="mt-3 text-center text-xs text-faint">
          If you don&apos;t confirm, the seller is paid automatically 72 hours after shipping.
        </p>
      ) : null}
    </Reveal>
  );
}
