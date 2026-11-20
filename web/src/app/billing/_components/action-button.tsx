import type { ReactNode } from "react";

export function ActionButton({
  action,
  children,
}: {
  action: () => Promise<void>;
  children: ReactNode;
}) {
  return (
    <form action={action}>
      <button
        type="submit"
        className="rounded-md border border-line-strong bg-surface px-4 py-2 font-medium hover:bg-surface-muted"
      >
        {children}
      </button>
    </form>
  );
}
