import Link from "next/link";

import { Badge } from "@/components/ui/badge";
import { MeetingTime } from "@/components/ui/date-time";
import { EmptyState } from "@/components/ui/empty-state";

import {
  attemptCount,
  attemptResult,
  eventLabel,
  formatDuration,
  type Delivery,
} from "../deliveries";

import { BUTTON } from "./styles";

function Summary({ delivery }: { delivery: Delivery }) {
  const { attempts } = delivery;
  const delivered = attempts.find((attempt) => attempt.succeeded);
  if (delivered !== undefined) {
    return (
      <>
        <MeetingTime value={delivered.startedAt} format="short-datetime" /> ·{" "}
        {String(delivered.statusCode)} in {formatDuration(delivered.durationMs)}{" "}
        on attempt {String(delivered.attempt)}
      </>
    );
  }
  const last = attempts.at(-1);
  const span =
    attempts.length > 1 ? (
      <>
        <MeetingTime value={delivery.firstAttemptAt} format="short-datetime" />{" "}
        to{" "}
        <MeetingTime value={delivery.lastAttemptAt} format="short-datetime" />
      </>
    ) : (
      <MeetingTime value={delivery.lastAttemptAt} format="short-datetime" />
    );
  return (
    <>
      {attemptCount(attempts.length)}, {span}
      {last === undefined ? null : <> · last: {attemptResult(last)}</>}
    </>
  );
}

function Attempts({ delivery }: { delivery: Delivery }) {
  if (delivery.attempts.length === 0) {
    return null;
  }
  return (
    <details className="text-sm">
      <summary className="inline-flex min-h-11 cursor-pointer items-center font-medium text-accent underline underline-offset-4">
        Attempts ({String(delivery.attempts.length)})
      </summary>
      <div className="mt-1 overflow-x-auto">
        <table className="w-full min-w-[28rem] text-left">
          <thead className="text-ink-muted">
            <tr className="border-b border-line">
              <th scope="col" className="py-1.5 pr-3 font-medium">
                #
              </th>
              <th scope="col" className="py-1.5 pr-3 font-medium">
                Sent
              </th>
              <th scope="col" className="py-1.5 pr-3 font-medium">
                Result
              </th>
              <th scope="col" className="py-1.5 font-medium">
                Took
              </th>
            </tr>
          </thead>
          <tbody>
            {delivery.attempts.map((attempt) => (
              <tr
                key={attempt.attempt}
                className="border-b border-line last:border-0"
              >
                <td className="py-2 pr-3 align-top tabular-nums">
                  {String(attempt.attempt)}
                </td>
                <td className="py-2 pr-3 align-top">
                  <MeetingTime
                    value={attempt.startedAt}
                    format="short-datetime"
                  />
                </td>
                <td className="py-2 pr-3 align-top break-words">
                  {attemptResult(attempt)}
                </td>
                <td className="py-2 align-top tabular-nums">
                  {formatDuration(attempt.durationMs)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </details>
  );
}

export function DeliveryLog({
  deliveries,
  agencyName,
  olderHref,
}: {
  deliveries: readonly Delivery[];
  agencyName: string;
  olderHref: string | null;
}) {
  if (deliveries.length === 0) {
    return (
      <EmptyState title="No deliveries yet">
        Deliveries show here once {agencyName} posts a meeting or an agenda.
        Send a test to check your endpoint now.
      </EmptyState>
    );
  }
  return (
    <div className="space-y-4">
      <ul className="divide-y divide-line rounded-lg border border-line bg-surface">
        {deliveries.map((delivery) => (
          <li key={delivery.eventId} className="space-y-1 px-4 py-4 sm:px-6">
            <p className="flex flex-wrap items-center gap-x-3 gap-y-1">
              <Badge tone={delivery.succeeded ? "held" : "cancelled"}>
                {delivery.succeeded ? "Delivered" : "Failed"}
              </Badge>{" "}
              <span className="font-semibold">{eventLabel(delivery.type)}</span>{" "}
              <span className="text-ink-muted">
                · {delivery.meeting.title},{" "}
                <MeetingTime
                  value={delivery.meeting.startsAt}
                  format="short-date"
                />
              </span>
            </p>
            <p className="text-sm text-ink-muted">
              <Summary delivery={delivery} />
            </p>
            <Attempts delivery={delivery} />
          </li>
        ))}
      </ul>
      {olderHref === null ? null : (
        <Link href={olderHref} className={`${BUTTON} w-full sm:w-auto`}>
          Older deliveries
        </Link>
      )}
    </div>
  );
}
