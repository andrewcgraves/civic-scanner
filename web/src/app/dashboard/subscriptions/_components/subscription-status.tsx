import { Badge } from "@/components/ui/badge";
import { Notice } from "@/components/ui/notice";

import { PAUSE_AFTER_FAILED_EVENTS, failureStreak } from "../deliveries";
import type { Subscription } from "../state";

export function StatusBadge({
  subscription,
}: {
  subscription: Pick<Subscription, "status" | "pausedReason">;
}) {
  if (subscription.status === "active") {
    return <Badge tone="held">Active</Badge>;
  }
  if (subscription.pausedReason === "failed_deliveries") {
    return <Badge tone="cancelled">Paused: deliveries failing</Badge>;
  }
  return <Badge tone="neutral">Paused</Badge>;
}

export function FailureStreak({
  count,
  compact = false,
}: {
  count: number;
  compact?: boolean;
}) {
  if (count <= 0) {
    return null;
  }
  const warning = `After ${String(PAUSE_AFTER_FAILED_EVENTS)} in a row, this subscription is paused.`;
  if (compact) {
    return (
      <p className="text-sm text-error-ink">
        {failureStreak(count)}. {warning}
      </p>
    );
  }
  return (
    <Notice tone="error" title={failureStreak(count)}>
      {warning} A delivered event resets the count.
    </Notice>
  );
}
