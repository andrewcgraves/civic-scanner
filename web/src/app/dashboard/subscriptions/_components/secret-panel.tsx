"use client";

import { useEffect, useId, useRef, useState, type ReactNode } from "react";

type CopyState = "idle" | "copied" | "failed";

const COPY_NOTES: Record<CopyState, string> = {
  idle: "",
  copied: "The secret is on your clipboard.",
  failed: "It could not be copied. Select the secret and copy it yourself.",
};

export function SecretPanel({
  title,
  secret,
  children,
  footer,
}: {
  title: string;
  secret: string;
  children: ReactNode;
  footer: ReactNode;
}) {
  const panel = useRef<HTMLDivElement>(null);
  const [copy, setCopy] = useState<CopyState>("idle");
  const id = useId();
  const inputId = `${id}-secret`;
  const hintId = `${id}-hint`;
  const warningId = `${id}-warning`;

  useEffect(() => {
    panel.current?.focus();
  }, []);

  async function copySecret() {
    try {
      await navigator.clipboard.writeText(secret);
      setCopy("copied");
    } catch {
      setCopy("failed");
    }
  }

  return (
    <div
      ref={panel}
      role="status"
      tabIndex={-1}
      className="space-y-4 rounded-lg border border-line bg-surface px-5 py-5 sm:px-6"
    >
      <div className="space-y-1">
        <p className="text-xl font-semibold">{title}</p>
        <div className="text-ink-muted">{children}</div>
      </div>
      <div>
        <label htmlFor={inputId} className="font-medium">
          Signing secret
        </label>
        <p id={hintId} className="text-sm text-ink-muted">
          Your receiver uses it to check that each webhook came from Civic
          Scanner.
        </p>
        <div className="mt-1 flex flex-col gap-2 sm:flex-row">
          <input
            id={inputId}
            type="text"
            readOnly
            value={secret}
            spellCheck={false}
            autoComplete="off"
            onFocus={(event) => {
              event.currentTarget.select();
            }}
            aria-describedby={`${hintId} ${warningId}`}
            className="block min-h-11 w-full min-w-0 rounded-md border border-line-strong bg-surface-muted px-3 py-2 font-mono text-sm text-ink sm:flex-1"
          />
          <button
            type="button"
            onClick={() => void copySecret()}
            className="min-h-11 rounded-md border border-line-strong bg-surface px-4 py-2 font-medium hover:bg-surface-muted"
          >
            {copy === "copied" ? "Copied" : "Copy secret"}
          </button>
        </div>
        <p
          aria-live="polite"
          className={`mt-1 min-h-5 text-sm ${copy === "failed" ? "text-error-ink" : "text-held-ink"}`}
        >
          {COPY_NOTES[copy]}
        </p>
      </div>
      <div
        id={warningId}
        className="rounded-lg border border-error-line bg-error-surface px-4 py-3 text-error-ink"
      >
        <p className="font-medium">This is the only time the secret is shown</p>
        <p className="mt-1 text-sm">
          Copy it now and store it where your receiver can read it. If it is
          lost, rotate the secret to get a new one.
        </p>
      </div>
      {footer}
    </div>
  );
}
