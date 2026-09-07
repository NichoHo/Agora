import Link from "next/link";

/** Shared tab strip between the two admin surfaces: assist's trust queue and
 * risk's console. Kept as its own component instead of a shared layout.tsx
 * so each page keeps its own independent auth check. */
export default function AdminNav({ active }: { active: "trust" | "risk" }) {
  const tabs = [
    { key: "trust", href: "/admin", label: "Trust" },
    { key: "risk", href: "/admin/risk", label: "Risk" },
  ] as const;
  return (
    <nav className="mb-6 flex gap-1 border-b border-line">
      {tabs.map((t) => (
        <Link
          key={t.key}
          href={t.href}
          className={`-mb-px border-b-2 px-3 py-2 text-sm font-medium ${
            active === t.key
              ? "border-primary text-ink"
              : "border-transparent text-muted-foreground hover:text-ink"
          }`}
        >
          {t.label}
        </Link>
      ))}
    </nav>
  );
}
