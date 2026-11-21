import { PageHeader } from "@/components/ui/page-header";
import { Section } from "@/components/ui/section";

function SkeletonRow() {
  return (
    <div aria-hidden="true" className="space-y-2.5">
      <div className="h-5 w-48 rounded bg-surface-muted" />
      <div className="h-3.5 w-4/5 rounded bg-surface-muted" />
      <div className="h-3.5 w-2/5 rounded bg-surface-muted" />
    </div>
  );
}

export default function SubscriptionsLoading() {
  return (
    <>
      <PageHeader
        title="Dashboard"
        description="Your plan, and what comes with it."
      />
      <Section title="Webhook subscriptions">
        <div
          aria-busy="true"
          className="space-y-5 rounded-lg border border-line bg-surface px-5 py-5 sm:px-6"
        >
          <p className="text-sm text-ink-muted">Loading your subscriptions…</p>
          <SkeletonRow />
          <div className="border-t border-line pt-5">
            <SkeletonRow />
          </div>
        </div>
      </Section>
    </>
  );
}
