"use client";

import { useActionState } from "react";

import { MeetingTime } from "@/components/ui/date-time";
import { Notice } from "@/components/ui/notice";

import { resumeSubscription } from "../actions";
import { PAUSE_AFTER_FAILED_EVENTS, type PausedReason } from "../deliveries";
import { IDLE_ACTION, RETURN_TO_SUBSCRIPTION } from "../state";

import { ActionProblem } from "./action-problem";
import { BUTTON } from "./styles";

function Since({ pausedAt }: { pausedAt: string }) {
  return <MeetingTime value={pausedAt} format="short-datetime" />;
}

function Title({
  reason,
  pausedAt,
}: {
  reason: PausedReason | null;
  pausedAt: string | null;
}) {
  if (reason === "failed_deliveries") {
    return (
      <>
        Paused after {String(PAUSE_AFTER_FAILED_EVENTS)} events in a row failed
        to deliver
      </>
    );
  }
  if (pausedAt === null) {
    return <>You paused this subscription</>;
  }
  return (
    <>
      You paused this subscription on <Since pausedAt={pausedAt} />
    </>
  );
}

function Body({
  reason,
  pausedAt,
  compact,
}: {
  reason: PausedReason | null;
  pausedAt: string | null;
  compact: boolean;
}) {
  const later = "Events while it is paused are not sent later.";
  if (reason !== "failed_deliveries") {
    return <>No webhooks are sent to it until you resume it. {later}</>;
  }
  if (compact) {
    return pausedAt === null ? (
      <>{later}</>
    ) : (
      <>
        Since <Since pausedAt={pausedAt} />. {later}
      </>
    );
  }
  return (
    <>
      {pausedAt === null ? (
        "Civic Scanner"
      ) : (
        <>
          On <Since pausedAt={pausedAt} />, Civic Scanner
        </>
      )}{" "}
      stopped sending to this URL: each of the last{" "}
      {String(PAUSE_AFTER_FAILED_EVENTS)} events failed every retry for about a
      day. {later} Fix your endpoint, check it with a test, then resume.
    </>
  );
}

export function PausedNotice({
  id,
  agencyName,
  reason,
  pausedAt,
  compact = false,
}: {
  id: string;
  agencyName: string;
  reason: PausedReason | null;
  pausedAt: string | null;
  compact?: boolean;
}) {
  const [state, resume, resuming] = useActionState(
    resumeSubscription,
    IDLE_ACTION,
  );
  const idleLabel = compact ? "Resume" : "Resume subscription";
  return (
    <div className="space-y-3">
      <Notice
        tone={reason === "failed_deliveries" ? "error" : "info"}
        title={<Title reason={reason} pausedAt={pausedAt} />}
      >
        <div className="space-y-3">
          <p className="max-w-[65ch]">
            <Body reason={reason} pausedAt={pausedAt} compact={compact} />
          </p>
          <form action={resume}>
            <input type="hidden" name="id" value={id} />
            {compact ? null : (
              <input
                type="hidden"
                name="returnTo"
                value={RETURN_TO_SUBSCRIPTION}
              />
            )}
            <button
              type="submit"
              disabled={resuming}
              aria-busy={resuming}
              aria-label={`Resume ${agencyName}`}
              className={`${BUTTON} text-ink`}
            >
              {resuming ? "Resuming…" : idleLabel}
            </button>
          </form>
        </div>
      </Notice>
      <ActionProblem state={state} />
    </div>
  );
}
