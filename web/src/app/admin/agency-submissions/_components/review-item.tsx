"use client";

import { useActionState } from "react";

import { MeetingTime } from "@/components/ui/date-time";
import { Notice } from "@/components/ui/notice";
import type { components } from "@/lib/api/v1";

import { approveSubmission, rejectSubmission } from "../actions";
import {
  SLUG_MAX_LENGTH,
  SLUG_RULE,
  suggestSlug,
  type ReviewState,
} from "../state";

type PendingSubmission = components["schemas"]["PendingAgencySubmission"];

const INPUT =
  "mt-1 block w-full max-w-sm rounded-md border border-line-strong bg-surface px-3 py-2 font-mono text-ink aria-invalid:border-error-line";
const BUTTON = "rounded-md px-4 py-2 font-medium disabled:opacity-60";

function slugError(state: ReviewState): string | null {
  switch (state.kind) {
    case "invalid-slug":
      return state.message;
    case "slug-taken":
      return "Another agency already has this slug. Choose a different one.";
    default:
      return null;
  }
}

function ProblemNotice({ state }: { state: ReviewState }) {
  switch (state.kind) {
    case "reviewed":
      return (
        <Notice tone="error">
          This agency has already been reviewed. Reload the page to see the
          current list.
        </Notice>
      );
    case "gone":
      return (
        <Notice tone="error">
          This agency could not be found. Reload the page to see the current
          list.
        </Notice>
      );
    case "not-allowed":
      return (
        <Notice tone="error">
          You can no longer review agencies. Your admin access may have been
          removed.
        </Notice>
      );
    case "unavailable":
      return (
        <Notice tone="error">
          The review could not be saved right now. Please try again later.
        </Notice>
      );
    default:
      return null;
  }
}

export function ReviewItem({ submission }: { submission: PendingSubmission }) {
  const [approval, approve, approving] = useActionState(approveSubmission, {
    kind: "idle",
    slug: suggestSlug(submission.name),
  });
  const [rejection, reject, rejecting] = useActionState(rejectSubmission, {
    kind: "idle",
    slug: "",
  });
  const busy = approving || rejecting;
  const headingId = `submission-${submission.id}`;
  const slugId = `slug-${submission.id}`;
  const hintId = `${slugId}-hint`;
  const errorId = `${slugId}-error`;
  const error = slugError(approval);

  return (
    <article
      aria-labelledby={headingId}
      className="space-y-4 rounded-lg border border-line px-5 py-4"
    >
      <div className="space-y-1">
        <h2 id={headingId} className="text-xl font-semibold">
          {submission.name}
        </h2>
        <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-sm">
          <dt className="text-ink-muted">Website</dt>
          <dd className="font-mono break-all">{submission.website}</dd>
          <dt className="text-ink-muted">Submitted by</dt>
          <dd className="break-all">
            {submission.submittedBy.email ?? "Unknown email"}
          </dd>
          <dt className="text-ink-muted">Submitted</dt>
          <dd>
            <MeetingTime
              value={submission.submittedAt}
              format="short-datetime"
            />
          </dd>
        </dl>
      </div>
      <ProblemNotice state={approval} />
      <ProblemNotice state={rejection} />
      <div className="flex flex-wrap items-end gap-x-8 gap-y-4">
        <form action={approve} className="space-y-2">
          <input type="hidden" name="id" value={submission.id} />
          <div>
            <label htmlFor={slugId} className="font-medium">
              Slug
            </label>
            <p id={hintId} className="text-sm text-ink-muted">
              {SLUG_RULE} It becomes the address /agencies/slug and the key of
              its source adapter.
            </p>
            <input
              id={slugId}
              name="slug"
              type="text"
              required
              maxLength={SLUG_MAX_LENGTH}
              autoComplete="off"
              spellCheck={false}
              defaultValue={approval.slug}
              aria-invalid={error === null ? undefined : true}
              aria-describedby={
                error === null ? hintId : `${hintId} ${errorId}`
              }
              className={INPUT}
            />
            {error === null ? null : (
              <p id={errorId} className="mt-1 text-sm text-error-ink">
                {error}
              </p>
            )}
          </div>
          <button
            type="submit"
            disabled={busy}
            className={`${BUTTON} bg-accent text-canvas hover:bg-accent-strong`}
          >
            {approving ? "Approving…" : "Approve and make live"}
          </button>
        </form>
        <form action={reject}>
          <input type="hidden" name="id" value={submission.id} />
          <button
            type="submit"
            disabled={busy}
            className={`${BUTTON} border border-error-line text-error-ink hover:bg-error-surface`}
          >
            {rejecting ? "Rejecting…" : "Reject"}
          </button>
        </form>
      </div>
    </article>
  );
}
