import type { ReactNode } from "react";

export type BadgeTone = "neutral" | "scheduled" | "held" | "cancelled" | "ai";

const TONES: Record<BadgeTone, string> = {
  neutral: "bg-surface-muted text-ink-muted",
  scheduled: "bg-scheduled-surface text-scheduled-ink",
  held: "bg-held-surface text-held-ink",
  cancelled: "bg-cancelled-surface text-cancelled-ink",
  ai: "bg-ai-surface text-ai-ink ring-1 ring-ai-line ring-inset",
};

export function Badge({
  tone = "neutral",
  children,
}: {
  tone?: BadgeTone;
  children: ReactNode;
}) {
  return (
    <span
      className={`inline-flex shrink-0 items-center rounded-full px-2.5 py-0.5 text-xs font-medium whitespace-nowrap ${TONES[tone]}`}
    >
      {children}
    </span>
  );
}
