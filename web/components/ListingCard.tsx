import Link from "next/link";
import { usd, type Listing } from "@/lib/api";

export default function ListingCard({ listing }: { listing: Listing }) {
  return (
    <Link
      href={`/listing/${listing.id}`}
      className="group block overflow-hidden rounded-card bg-surface shadow-sm transition-[transform,box-shadow] duration-200 hover:-translate-y-1 hover:shadow-lg"
    >
      <div className="aspect-[4/3] overflow-hidden bg-fill">
        {/* plain <img>: no image optimizer in the container; picsum seeds the demo */}
        <img
          src={listing.image_url || "https://picsum.photos/seed/vault/800/600"}
          alt={listing.title}
          width={800}
          height={600}
          loading="lazy"
          className="h-full w-full object-cover transition-transform duration-500 ease-out group-hover:scale-105"
        />
      </div>
      <div className="p-4">
        <p className="truncate text-sm leading-5 text-ink-2 transition-colors group-hover:text-ink">
          {listing.title}
        </p>
        <p className="money mt-1.5 text-base font-semibold text-ink">{usd(listing.price_minor)}</p>
      </div>
    </Link>
  );
}
