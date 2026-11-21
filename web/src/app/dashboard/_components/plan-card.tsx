import { ActionButton } from "@/app/billing/_components/action-button";
import { Badge } from "@/components/ui/badge";
import type { UserPlan } from "@/lib/auth/session-state";
import {
  openBillingPortalAction,
  startCheckoutAction,
} from "@/lib/billing/actions";

const PLANS = {
  free: {
    title: "Free plan",
    tone: "neutral",
    description:
      "Every agency, meeting, agenda and transcript on Civic Scanner is free to read. The paid plan adds webhook subscriptions, which are coming soon.",
    liveDescription:
      "Every agency, meeting, agenda and transcript on Civic Scanner is free to read. The paid plan adds webhook subscriptions.",
    action: startCheckoutAction,
    label: "Start the paid plan",
    hint: "Payment is taken on a secure Stripe page.",
  },
  paid: {
    title: "Paid plan",
    tone: "held",
    description:
      "Thank you for supporting Civic Scanner. Webhook subscriptions are included in your plan when they arrive.",
    liveDescription:
      "Thank you for supporting Civic Scanner. Webhook subscriptions are included in your plan.",
    action: openBillingPortalAction,
    label: "Open the billing portal",
    hint: "Change your card, see invoices or cancel in Stripe's billing portal.",
  },
} as const;

export function PlanCard({
  plan,
  billing,
  subscriptions = false,
}: {
  plan: UserPlan;
  billing: boolean;
  subscriptions?: boolean;
}) {
  const { title, tone, description, liveDescription, action, label, hint } =
    PLANS[plan];
  return (
    <div className="space-y-4 rounded-lg border border-line bg-surface px-5 py-5 sm:px-6">
      <div className="flex flex-wrap items-center gap-3">
        <p className="text-xl font-semibold">{title}</p>
        <Badge tone={tone}>Current</Badge>
      </div>
      <p className="max-w-[62ch] text-ink-muted">
        {subscriptions ? liveDescription : description}
      </p>
      {billing ? (
        <div className="flex flex-col items-start gap-2 sm:flex-row sm:items-center sm:gap-4">
          <ActionButton action={action}>{label}</ActionButton>
          <p className="text-sm text-ink-muted">{hint}</p>
        </div>
      ) : null}
    </div>
  );
}

export function PlanCardLoading() {
  return (
    <div
      aria-busy="true"
      className="space-y-4 rounded-lg border border-line bg-surface px-5 py-5 sm:px-6"
    >
      <p className="text-sm text-ink-muted">Loading your plan…</p>
      <div aria-hidden="true" className="space-y-4">
        <div className="h-6 w-36 rounded bg-surface-muted" />
        <div className="space-y-2">
          <div className="h-3.5 w-full rounded bg-surface-muted" />
          <div className="h-3.5 w-5/6 rounded bg-surface-muted" />
        </div>
        <div className="h-10 w-44 rounded-md bg-surface-muted" />
      </div>
    </div>
  );
}
