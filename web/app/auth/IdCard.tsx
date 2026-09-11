import Reveal from "@/components/motion/Reveal";

// Shared shell for IdP screens. Visually distinct from the storefront, because
// the identity provider is its own product.
export default function IdCard({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}) {
  return (
    <Reveal mode="mount" className="mx-auto mt-12 max-w-sm">
      <div className="mb-4 text-center">
        <span className="rounded-control bg-primary px-2 py-1 text-xs font-bold tracking-widest text-on-solid">
          VAULT ID
        </span>
      </div>
      <div className="rounded-panel border border-line bg-surface p-8 shadow-md">
        <h1 className="mb-5 text-xl font-bold tracking-tight text-ink">{title}</h1>
        {children}
      </div>
    </Reveal>
  );
}
