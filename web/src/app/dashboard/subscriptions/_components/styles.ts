export const BUTTON =
  "inline-flex min-h-11 items-center justify-center rounded-md border border-line-strong bg-surface px-4 py-2 font-medium text-ink hover:bg-surface-muted disabled:cursor-wait disabled:opacity-60";

export const DANGER_BUTTON =
  "inline-flex min-h-11 items-center justify-center rounded-md border border-error-ink bg-error-ink px-4 py-2 font-medium text-surface hover:opacity-90 disabled:cursor-wait disabled:opacity-60";

const ROW_ACTION =
  "inline-flex min-h-11 items-center justify-center rounded-md border px-3 text-sm font-medium sm:border-0 sm:bg-transparent sm:px-0.5 sm:underline sm:underline-offset-4 disabled:cursor-wait disabled:opacity-60";

export const ROW_ACTION_LINK = `${ROW_ACTION} border-line-strong bg-surface text-ink sm:text-accent sm:hover:text-accent-strong`;

export const ROW_ACTION_DANGER = `${ROW_ACTION} border-error-line bg-surface text-error-ink`;

export const INPUT =
  "mt-1 block min-h-11 w-full rounded-md border border-line-strong bg-surface px-3 py-2 text-ink aria-invalid:border-error-ink";
