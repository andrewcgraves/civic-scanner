export default function Loading() {
  return (
    <div role="status" aria-live="polite" className="animate-pulse space-y-4">
      <span className="sr-only">Loading…</span>
      <div aria-hidden="true" className="h-9 w-2/3 rounded bg-surface-muted" />
      <div aria-hidden="true" className="h-5 w-1/2 rounded bg-surface-muted" />
      <div
        aria-hidden="true"
        className="mt-10 h-40 rounded-lg bg-surface-muted"
      />
    </div>
  );
}
