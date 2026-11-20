import type { ReactNode } from "react";

const TONES = {
  error: "border-error-line bg-error-surface text-error-ink",
  info: "border-line bg-surface-muted text-ink",
} as const;

export function Notice({
  tone = "info",
  title,
  children,
}: {
  tone?: keyof typeof TONES;
  title?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div
      role={tone === "error" ? "alert" : undefined}
      className={`rounded-lg border px-4 py-3 ${TONES[tone]}`}
    >
      {title === undefined ? null : <p className="font-medium">{title}</p>}
      <div className={title === undefined ? undefined : "mt-1 text-sm"}>
        {children}
      </div>
    </div>
  );
}

export function UnavailableNotice({ what }: { what: string }) {
  return (
    <Notice tone="error">
      {what} unavailable right now. Please try again later.
    </Notice>
  );
}
