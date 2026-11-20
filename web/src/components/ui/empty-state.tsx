import type { ReactNode } from "react";

export function EmptyState({
  title,
  children,
}: {
  title: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="rounded-lg border border-dashed border-line-strong px-5 py-8 text-center">
      <p className="font-medium">{title}</p>
      {children === undefined ? null : (
        <div className="mx-auto mt-2 max-w-[55ch] text-sm text-ink-muted">
          {children}
        </div>
      )}
    </div>
  );
}
