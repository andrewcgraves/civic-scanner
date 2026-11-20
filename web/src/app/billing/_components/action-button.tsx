"use client";

import type { ReactNode } from "react";
import { useFormStatus } from "react-dom";

function SubmitButton({ children }: { children: ReactNode }) {
  const { pending } = useFormStatus();
  return (
    <button
      type="submit"
      disabled={pending}
      aria-busy={pending}
      className="rounded-md border border-line-strong bg-surface px-4 py-2 font-medium hover:bg-surface-muted disabled:cursor-wait disabled:opacity-60"
    >
      {pending ? "Opening Stripe…" : children}
    </button>
  );
}

export function ActionButton({
  action,
  children,
}: {
  action: () => Promise<void>;
  children: ReactNode;
}) {
  return (
    <form action={action}>
      <SubmitButton>{children}</SubmitButton>
    </form>
  );
}
