import Link from "next/link";
import { redirect } from "next/navigation";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import Reveal from "@/components/motion/Reveal";
import ServiceUnavailable from "@/components/ServiceUnavailable";
import { fetchWallet, payDeposit, usd, type Wallet } from "@/lib/api";
import { getToken } from "@/lib/auth";

async function topUpAction() {
  "use server";
  const token = await getToken();
  if (!token) redirect("/auth/start");
  await payDeposit(token, 50_000);
  redirect("/wallet");
}

const kindLabel: Record<string, string> = {
  deposit: "Deposit",
  escrow_fund: "Payment to escrow",
  escrow_release: "Sale proceeds",
  escrow_refund: "Refund",
  fee: "Platform fee",
};

export default async function WalletPage() {
  const token = await getToken();
  if (!token) redirect("/auth/start?next=/wallet");
  // A pay-service outage must never render as a $0.00 balance, which reads as
  // "your money is gone". Keep unreachable distinct from unauthorized.
  let wallet: Wallet | null = null;
  let unreachable = false;
  try {
    wallet = await fetchWallet(token);
  } catch {
    unreachable = true;
  }

  if (unreachable) {
    return (
      <div className="mx-auto max-w-md">
        <h1 className="mb-4 text-xl font-bold tracking-tight text-ink">Wallet</h1>
        <ServiceUnavailable
          service="wallet"
          detail="Your balance and history can’t be loaded right now. This is not a zero balance, and no funds are affected."
        />
      </div>
    );
  }
  if (!wallet) redirect("/auth/start?next=/wallet");

  return (
    <Reveal mode="mount" className="mx-auto max-w-md">
      <h1 className="mb-4 text-xl font-bold tracking-tight text-ink">Wallet</h1>
      <Card>
        <CardContent className="text-center">
          <p className="text-sm text-muted-foreground">Balance</p>
          <p className="money text-4xl font-bold text-ink">{usd(wallet.balance_minor)}</p>
          <form action={topUpAction} className="mt-4">
            <Button type="submit">Add $500 demo funds</Button>
          </form>
        </CardContent>
      </Card>

      <h2 className="mb-2 mt-6 text-sm font-bold text-muted-foreground">
        Every payment in and out of your account
      </h2>
      {wallet.entries.length === 0 ? (
        <p className="text-sm text-muted-foreground">No activity yet.</p>
      ) : (
        <ul className="divide-y divide-line rounded-card border border-line bg-surface">
          {wallet.entries.map((e, i) => {
            const orderID = e.reference.startsWith("order:") ? e.reference.slice(6) : null;
            return (
              <li key={i} className="flex items-center justify-between px-4 py-2.5 text-sm">
                <div>
                  <p className="text-ink">{kindLabel[e.kind] ?? e.kind}</p>
                  <p className="text-xs text-faint">
                    {new Date(e.created_at).toLocaleString()}
                    {orderID ? (
                      <>
                        {" · "}
                        <Link href={`/orders/${orderID}`} className="text-primary underline">
                          order
                        </Link>
                      </>
                    ) : null}
                  </p>
                </div>
                <span
                  className={`money font-medium ${e.amount_minor > 0 ? "text-success" : "text-danger"}`}
                >
                  {e.amount_minor > 0 ? "+" : ""}
                  {usd(e.amount_minor)}
                </span>
              </li>
            );
          })}
        </ul>
      )}
    </Reveal>
  );
}
