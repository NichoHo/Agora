import { notFound, redirect } from "next/navigation";
import Reveal from "@/components/motion/Reveal";
import StatusBadge from "@/components/StatusBadge";
import { fetchCategories, fetchListing, marketPost, usd } from "@/lib/api";
import { getToken, getUser } from "@/lib/auth";

async function buyNow(formData: FormData) {
  "use server";
  const token = await getToken();
  const listingID = String(formData.get("listing_id"));
  if (!token) redirect(`/auth/start?next=/listing/${listingID}`);
  const resp = await marketPost(token, "/orders", { listing_id: listingID });
  if (!resp.ok) redirect(`/listing/${listingID}?error=unavailable`);
  const order = (await resp.json()) as { id: string };
  redirect(`/checkout/${order.id}`);
}

export default async function ListingPage({
  params,
  searchParams,
}: {
  params: Promise<{ id: string }>;
  searchParams: Promise<{ error?: string }>;
}) {
  const { id } = await params;
  const [listing, categories, user, sp] = await Promise.all([
    fetchListing(id),
    fetchCategories(),
    getUser(),
    searchParams,
  ]);
  if (!listing) notFound();
  const category = categories.find((c) => c.id === listing.category_id);
  const isOwner = user?.sub === listing.seller_id;
  const buyable = listing.status === "active" && !isOwner;

  return (
    <div className="grid gap-8 md:grid-cols-2">
      <Reveal
        mode="mount"
        className="overflow-hidden rounded-panel border border-line bg-fill md:sticky md:top-20 md:self-start"
      >
        <img
          src={listing.image_url || "https://picsum.photos/seed/vault/800/600"}
          alt={listing.title}
          className="aspect-[4/3] w-full object-cover"
        />
      </Reveal>
      <Reveal mode="mount" delay={0.1}>
        <div className="mb-2 flex items-center gap-2">
          <StatusBadge status={listing.status} />
          {category ? <span className="text-xs text-faint">{category.name}</span> : null}
        </div>
        <h1 className="text-2xl font-bold tracking-tight text-ink">{listing.title}</h1>
        <p className="money mt-2 text-3xl font-bold text-ink">{usd(listing.price_minor)}</p>
        <p className="mt-4 whitespace-pre-wrap text-sm leading-6 text-muted-foreground">
          {listing.description || "No description."}
        </p>
        <div className="mt-6 rounded-card border border-line bg-surface p-4 text-sm">
          <p className="text-faint">Seller</p>
          <p className="font-medium text-ink">
            {isOwner ? "You" : listing.seller_id.slice(0, 8) + "…"}
          </p>
        </div>
        {sp.error === "unavailable" ? (
          <p className="mt-4 rounded-control bg-warning-tint px-3 py-2 text-sm text-warning">
            Someone else got there first. This listing is no longer available.
          </p>
        ) : null}
        {buyable ? (
          <form action={buyNow}>
            <input type="hidden" name="listing_id" value={listing.id} />
            <button
              type="submit"
              className="mt-6 w-full rounded-control bg-primary px-4 py-2.5 font-semibold text-on-solid shadow-sm transition-colors hover:bg-primary-strong"
            >
              Buy with escrow protection
            </button>
            <p className="mt-2 text-center text-xs text-faint">
              We hold your payment until you confirm the item arrived. Only then does the
              seller get paid.
            </p>
          </form>
        ) : (
          <p className="mt-6 text-center text-sm text-faint">
            {isOwner
              ? "This is your listing."
              : listing.status === "sold"
                ? "This item has been sold."
                : listing.status === "reserved"
                  ? "This item is reserved by another buyer."
                  : "This item is not for sale right now."}
          </p>
        )}
      </Reveal>
    </div>
  );
}
