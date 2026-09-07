import { notFound } from "next/navigation";
import { Package, ShoppingBag, ArrowLeftRight, LogIn, ShieldAlert, type LucideIcon } from "lucide-react";
import Reveal from "@/components/motion/Reveal";
import AdminNav from "@/components/admin/AdminNav";
import { getToken, getUser } from "@/lib/auth";
import { ADMIN_EMAILS, RISK_URL } from "@/lib/env";
import { resolveScoreAction } from "./actions";

type Metrics = {
  scored_by_decision: Record<string, number>;
  review_queue_open: number;
};

type Score = {
  id: number;
  subject_type: string;
  subject_id: string;
  score: number;
  decision: "allow" | "review" | "block";
  reasons: string[];
  model_version: string;
  status: string;
  created_at: string;
};

const SUBJECT_ICON: Record<string, LucideIcon> = {
  reservation: Package,
  order: ShoppingBag,
  transfer: ArrowLeftRight,
  login: LogIn,
  refresh_reuse: ShieldAlert,
};

const DECISION_STYLE: Record<Score["decision"], string> = {
  allow: "bg-success text-on-solid",
  review: "bg-warning text-on-solid",
  block: "bg-danger text-on-solid",
};

async function fetchAdmin<T>(token: string, path: string): Promise<T | null> {
  try {
    const resp = await fetch(`${RISK_URL}${path}`, {
      headers: { Authorization: `Bearer ${token}` },
      cache: "no-store",
    });
    if (!resp.ok) return null;
    return (await resp.json()) as T;
  } catch {
    return null;
  }
}

export default async function RiskPage() {
  const [user, token] = await Promise.all([getUser(), getToken()]);
  if (!user || !token || !ADMIN_EMAILS.includes(user.email.toLowerCase())) notFound();

  const [metrics, scores] = await Promise.all([
    fetchAdmin<Metrics>(token, "/admin/metrics"),
    fetchAdmin<Score[]>(token, "/admin/scores"),
  ]);

  return (
    <Reveal mode="mount" className="mx-auto max-w-3xl">
      <AdminNav active="risk" />
      <h1 className="mb-4 text-xl font-bold tracking-tight text-ink">Risk</h1>

      {!metrics ? (
        <p className="text-sm text-muted-foreground">The risk service is unreachable.</p>
      ) : (
        <>
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <Stat label="Allowed" value={String(metrics.scored_by_decision.allow ?? 0)} />
            <Stat label="Flagged for review" value={String(metrics.scored_by_decision.review ?? 0)} />
            <Stat label="Blocked" value={String(metrics.scored_by_decision.block ?? 0)} />
            <Stat label="Open queue" value={String(metrics.review_queue_open)} highlight />
          </div>

          <h2 className="mb-2 mt-6 text-sm font-bold text-muted-foreground">Anomaly feed</h2>
          {!scores || scores.length === 0 ? (
            <p className="text-sm text-muted-foreground">Nothing scored yet. Quiet day.</p>
          ) : (
            <ul className="divide-y divide-line rounded-card border border-line bg-surface">
              {scores.map((s) => {
                const Icon = SUBJECT_ICON[s.subject_type] ?? Package;
                return (
                  <li key={s.id} className="flex items-start gap-3 px-4 py-3 text-sm">
                    <span
                      className={`money mt-0.5 rounded-control px-2 py-0.5 text-xs font-bold ${DECISION_STYLE[s.decision]}`}
                    >
                      {s.score.toFixed(2)}
                    </span>
                    <div className="min-w-0 flex-1">
                      <p className="flex items-center gap-1.5 truncate text-ink">
                        <Icon aria-hidden="true" className="size-3.5 shrink-0 text-faint" strokeWidth={2} />
                        {s.subject_type} <code className="text-xs text-faint">{s.subject_id.slice(0, 8)}</code>
                      </p>
                      {s.reasons.length > 0 && (
                        <div className="mt-1 flex flex-wrap gap-1">
                          {s.reasons.map((reason, i) => (
                            <span
                              key={i}
                              className="rounded-control bg-fill px-1.5 py-0.5 text-xs text-ink-2"
                            >
                              {reason}
                            </span>
                          ))}
                        </div>
                      )}
                    </div>
                    {s.status === "queued" ? (
                      <div className="flex shrink-0 gap-2">
                        <form action={resolveScoreAction}>
                          <input type="hidden" name="id" value={s.id} />
                          <input type="hidden" name="action" value="approve" />
                          <button className="rounded-control bg-success px-2 py-1 text-xs text-on-solid hover:bg-success/80">
                            Allow
                          </button>
                        </form>
                        <form action={resolveScoreAction}>
                          <input type="hidden" name="id" value={s.id} />
                          <input type="hidden" name="action" value="reject" />
                          <button className="rounded-control bg-danger px-2 py-1 text-xs text-on-solid hover:bg-danger/80">
                            Block
                          </button>
                        </form>
                      </div>
                    ) : (
                      <span className="shrink-0 text-xs text-faint">{s.status}</span>
                    )}
                  </li>
                );
              })}
            </ul>
          )}
        </>
      )}
    </Reveal>
  );
}

function Stat({ label, value, highlight }: { label: string; value: string; highlight?: boolean }) {
  return (
    <div className="rounded-card border border-line bg-surface p-3">
      <p className="text-xs text-faint">{label}</p>
      <p className={`money text-2xl font-bold ${highlight ? "text-warning" : "text-ink"}`}>{value}</p>
    </div>
  );
}
