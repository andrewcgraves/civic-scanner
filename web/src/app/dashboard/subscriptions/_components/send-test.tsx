"use client";

import { useActionState, useEffect, useId, useState } from "react";

import { Badge } from "@/components/ui/badge";
import { MeetingTime } from "@/components/ui/date-time";
import { Notice } from "@/components/ui/notice";

import { sendTest } from "../actions";
import {
  DEFAULT_EVENT_TYPE,
  EVENT_LABELS,
  EVENT_TYPES,
  IDLE_TEST,
  formatDuration,
  problem,
  type TestResult,
  type TestState,
} from "../deliveries";

import { BUTTON, INPUT } from "./styles";

function Sent({ result }: { result: TestResult }) {
  const sentAt = (
    <>
      Sent <MeetingTime value={result.sentAt} format="short-datetime" />.
    </>
  );
  if (result.succeeded) {
    return (
      <Notice
        title={
          <span className="flex items-center gap-2">
            <Badge tone="held">Delivered</Badge>
            Test delivered
          </span>
        }
      >
        Your endpoint answered {String(result.statusCode)} in{" "}
        {formatDuration(result.durationMs)}. {sentAt}
      </Notice>
    );
  }
  const why = problem(result.error);
  return (
    <div className="rounded-lg border border-error-line bg-error-surface px-4 py-3 text-error-ink">
      <p className="flex items-center gap-2 font-medium">
        <Badge tone="cancelled">Failed</Badge>
        Test not delivered
      </p>
      <p className="mt-1 text-sm">
        {result.statusCode === null ? (
          <>
            {why === undefined ? "There was no answer" : `No answer: ${why}`}.
          </>
        ) : (
          <>
            Your endpoint answered {String(result.statusCode)} in{" "}
            {formatDuration(result.durationMs)}. Any 2xx counts as delivered.
          </>
        )}{" "}
        {sentAt}
      </p>
    </div>
  );
}

function Outcome({ state, waiting }: { state: TestState; waiting: boolean }) {
  switch (state.kind) {
    case "sent":
      return <Sent result={state.result} />;
    case "limited":
      return (
        <Notice title="You have sent 10 tests in the last minute">
          {waiting
            ? `You can send another in ${String(state.retryAfter)} seconds.`
            : "You can send another test now."}
        </Notice>
      );
    case "gone":
      return (
        <Notice tone="error">
          This subscription no longer exists. Reload the page to see your
          current subscriptions.
        </Notice>
      );
    case "upgrade":
      return (
        <Notice tone="error">
          Subscriptions need the paid plan. Start it again to send a test.
        </Notice>
      );
    case "unavailable":
      return (
        <Notice tone="error">
          The test could not be sent right now. Please try again later.
        </Notice>
      );
    default:
      return null;
  }
}

export function SendTest({ id }: { id: string }) {
  const [state, send, sending] = useActionState(sendTest, IDLE_TEST);
  const [waited, setWaited] = useState<TestState | null>(null);
  const selectId = useId();

  useEffect(() => {
    if (state.kind !== "limited") {
      return undefined;
    }
    const timer = setTimeout(() => {
      setWaited(state);
    }, state.retryAfter * 1000);
    return () => {
      clearTimeout(timer);
    };
  }, [state]);

  const waiting = state.kind === "limited" && waited !== state;

  return (
    <div className="space-y-4">
      <form action={send} className="flex flex-wrap items-end gap-3">
        <input type="hidden" name="id" value={id} />
        <div>
          <label htmlFor={selectId} className="text-sm font-medium">
            Event
          </label>
          <select
            id={selectId}
            name="type"
            defaultValue={DEFAULT_EVENT_TYPE}
            disabled={sending}
            className={`${INPUT} sm:w-60`}
          >
            {EVENT_TYPES.map((type) => (
              <option key={type} value={type}>
                {EVENT_LABELS[type]}
              </option>
            ))}
          </select>
        </div>
        <button
          type="submit"
          disabled={sending || waiting}
          aria-busy={sending}
          className={`${BUTTON} w-full sm:w-auto`}
        >
          {sending ? "Sending…" : "Send a test"}
        </button>
      </form>
      <div role="status">
        <Outcome state={state} waiting={waiting} />
      </div>
    </div>
  );
}
