import Link from "next/link";
import ListingCard from "@/components/ListingCard";
import StaggerGrid from "@/components/motion/StaggerGrid";
import { fetchCategories, fetchListings } from "@/lib/api";

export default async function SearchPage({
  searchParams,
}: {
  searchParams: Promise<{ q?: string; category?: string }>;
}) {
  const sp = await searchParams;
  const [{ items }, categories] = await Promise.all([
    fetchListings({ q: sp.q, category: sp.category, limit: 48 }),
    fetchCategories(),
  ]);

  const linkClass = (active: boolean) =>
    `block shrink-0 whitespace-nowrap rounded-control px-3 py-2 text-sm font-medium transition-colors ${
      active
        ? "bg-primary-tint text-primary"
        : "text-muted-foreground hover:bg-fill hover:text-ink"
    }`;

  return (
    <div className="grid gap-8 md:grid-cols-[200px_1fr]">
      {categories.length > 0 ? (
        <aside className="flex gap-2 overflow-x-auto pb-1 md:flex-col md:overflow-visible md:pb-0 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
          <Link href="/search" className={linkClass(!sp.category)}>
            All categories
          </Link>
          {categories.map((c) => (
            <Link
              key={c.id}
              href={`/search?category=${c.id}`}
              className={linkClass(sp.category === String(c.id))}
            >
              {c.name}
            </Link>
          ))}
        </aside>
      ) : null}
      <div>
        <h1 className="mb-4 text-xl font-bold tracking-tight text-ink">
          {sp.q ? `Results for “${sp.q}”` : "All listings"}
          <span className="ml-2 text-sm font-normal text-faint">{items.length} found</span>
        </h1>
        {items.length === 0 ? (
          <p className="text-muted-foreground">No listings match. Try another search.</p>
        ) : (
          // Framer Motion's whileInView + `once: true` (see StaggerGrid) only ever
          // fires once per component instance. Clicking a category tab is a
          // client-side navigation on the same route, so React reuses this same
          // instance instead of remounting it -- the new items would mount
          // already "seen" and sit stuck at opacity: 0 forever. A key tied to the
          // query forces a real remount (a fresh IntersectionObserver) whenever
          // the result set actually changes.
          <StaggerGrid
            key={`${sp.q ?? ""}:${sp.category ?? ""}`}
            className="grid grid-cols-2 gap-4 sm:grid-cols-3 sm:gap-6 lg:grid-cols-4"
          >
            {items.map((l) => (
              <ListingCard key={l.id} listing={l} />
            ))}
          </StaggerGrid>
        )}
      </div>
    </div>
  );
}
