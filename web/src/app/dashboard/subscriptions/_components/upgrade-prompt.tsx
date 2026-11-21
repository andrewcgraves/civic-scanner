import { ActionButton } from "@/app/billing/_components/action-button";
import { Badge } from "@/components/ui/badge";
import { startCheckoutAction } from "@/lib/billing/actions";

export function UpgradePrompt({ billing }: { billing: boolean }) {
  return (
    <div className="space-y-4 rounded-lg border border-line bg-surface px-5 py-5 sm:px-6">
      <div className="flex flex-wrap items-center gap-3">
        <p className="text-xl font-semibold">
          Subscriptions come with the paid plan
        </p>
        <Badge tone="scheduled">Paid</Badge>
      </div>
      <p className="max-w-[62ch] text-ink-muted">
        Choose agencies and get a signed webhook at your own URL each time one
        posts a meeting or an agenda. Up to 25 subscriptions.
      </p>
      {billing ? (
        <div className="flex flex-col items-start gap-2 sm:flex-row sm:items-center sm:gap-4">
          <ActionButton action={startCheckoutAction}>
            Start the paid plan
          </ActionButton>
          <p className="text-sm text-ink-muted">
            Payment is taken on a secure Stripe page.
          </p>
        </div>
      ) : (
        <p className="text-sm text-ink-muted">The paid plan is not open yet.</p>
      )}
    </div>
  );
}
