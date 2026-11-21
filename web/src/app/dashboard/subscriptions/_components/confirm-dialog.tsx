"use client";

import { useEffect, useId, useRef, type ReactNode } from "react";

import { BUTTON, DANGER_BUTTON } from "./styles";

export function ConfirmDialog({
  triggerLabel,
  triggerAccessibleName,
  triggerClassName,
  title,
  children,
  confirmLabel,
  pendingLabel,
  cancelLabel,
  danger = false,
  action,
  pending,
  closeWhen,
  fields,
}: {
  triggerLabel: string;
  triggerAccessibleName: string;
  triggerClassName: string;
  title: string;
  children: ReactNode;
  confirmLabel: string;
  pendingLabel: string;
  cancelLabel: string;
  danger?: boolean;
  action: (formData: FormData) => void;
  pending: boolean;
  closeWhen: unknown;
  fields: Record<string, string>;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const titleId = useId();

  useEffect(() => {
    dialog.current?.close();
  }, [closeWhen]);

  return (
    <>
      <button
        type="button"
        aria-label={triggerAccessibleName}
        onClick={() => dialog.current?.showModal()}
        className={triggerClassName}
      >
        {triggerLabel}
      </button>
      <dialog
        ref={dialog}
        aria-labelledby={titleId}
        className="m-auto w-[min(30rem,calc(100%-2rem))] rounded-lg bg-surface p-6 text-ink shadow-xl backdrop:bg-black/45 max-sm:mb-0 max-sm:w-full max-sm:max-w-none max-sm:rounded-b-none"
      >
        <form action={action} className="space-y-4">
          {Object.entries(fields).map(([name, value]) => (
            <input key={name} type="hidden" name={name} value={value} />
          ))}
          <h2 id={titleId} className="text-xl font-semibold">
            {title}
          </h2>
          <div className="space-y-3 text-ink-muted">{children}</div>
          <div className="flex flex-col-reverse gap-3 sm:flex-row sm:justify-end">
            <button
              type="button"
              onClick={() => dialog.current?.close()}
              className={BUTTON}
            >
              {cancelLabel}
            </button>
            <button
              type="submit"
              disabled={pending}
              aria-busy={pending}
              className={danger ? DANGER_BUTTON : BUTTON}
            >
              {pending ? pendingLabel : confirmLabel}
            </button>
          </div>
        </form>
      </dialog>
    </>
  );
}
