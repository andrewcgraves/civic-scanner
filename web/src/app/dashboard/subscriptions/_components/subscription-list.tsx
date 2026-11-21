import Link from "next/link";

import { Badge } from "@/components/ui/badge";
import { MeetingTime } from "@/components/ui/date-time";
import { EmptyState } from "@/components/ui/empty-state";

import {
  MAX_SUBSCRIPTIONS,
  NEW_SUBSCRIPTION_PATH,
  agencyName,
  type Subscription,
} from "../state";

import { SubscriptionActions } from "./subscription-actions";
import { BUTTON } from "./styles";

export function AddButton({ count }: { count: number }) {
  if (count >= MAX_SUBSCRIPTIONS) {
    return (
      <button
        type="button"
        disabled
        aria-describedby="subscription-limit"
        className={`${BUTTON} w-full sm:w-auto`}
      >
        Add a subscription
      </button>
    );
  }
  return (
    <Link href={NEW_SUBSCRIPTION_PATH} className={`${BUTTON} w-full sm:w-auto`}>
      Add a subscription
    </Link>
  );
}

function usage(count: number): string {
  if (count >= MAX_SUBSCRIPTIONS) {
    return `You are using all ${String(MAX_SUBSCRIPTIONS)}. Delete one to add another.`;
  }
  return `You are using ${String(count)} of ${String(MAX_SUBSCRIPTIONS)}.`;
}

export function SubscriptionList({
  subscriptions,
  names,
}: {
  subscriptions: readonly Subscription[];
  names: ReadonlyMap<string, string>;
}) {
  if (subscriptions.length === 0) {
    return (
      <EmptyState title="You have no subscriptions yet">
        Add one to get a webhook each time an agency posts a meeting or an
        agenda.{" "}
        <Link href={NEW_SUBSCRIPTION_PATH} className="link">
          Add a subscription
        </Link>
      </EmptyState>
    );
  }
  return (
    <div className="space-y-4">
      <p id="subscription-limit" className="max-w-[65ch] text-ink-muted">
        {usage(subscriptions.length)}
      </p>
      <ul className="space-y-3 sm:space-y-0 sm:divide-y sm:divide-line sm:rounded-lg sm:border sm:border-line sm:bg-surface">
        {subscriptions.map((subscription) => {
          const name = agencyName(names, subscription.agency);
          const paused = subscription.status === "paused";
          return (
            <li
              key={subscription.id}
              className="space-y-1.5 rounded-lg border border-line bg-surface px-4 py-4 sm:rounded-none sm:border-0 sm:bg-transparent sm:px-6 sm:py-5"
            >
              <div className="flex flex-wrap items-center justify-between gap-2 sm:justify-start sm:gap-3">
                <h3 className="text-lg font-semibold">{name}</h3>
                <Badge tone={paused ? "neutral" : "held"}>
                  {paused ? "Paused" : "Active"}
                </Badge>
              </div>
              <p
                className={`font-mono text-sm break-all ${paused ? "text-ink-muted" : "text-ink"}`}
              >
                {subscription.url}
              </p>
              <p className="text-sm text-ink-muted">
                Added{" "}
                <MeetingTime
                  value={subscription.createdAt}
                  format="short-date"
                />{" "}
                · secret made{" "}
                <MeetingTime
                  value={subscription.secretRotatedAt}
                  format="short-date"
                />
              </p>
              <SubscriptionActions
                key={subscription.status}
                id={subscription.id}
                agency={subscription.agency}
                agencyName={name}
                url={subscription.url}
                paused={paused}
              />
            </li>
          );
        })}
      </ul>
      <p className="text-sm text-ink-muted">
        Pausing and resuming take effect at once. To move a subscription to
        another agency, delete it and add a new one.
      </p>
    </div>
  );
}
