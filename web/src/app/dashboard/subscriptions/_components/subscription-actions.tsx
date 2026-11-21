"use client";

import Link from "next/link";
import { useActionState, useState } from "react";

import {
  deleteSubscription,
  pauseSubscription,
  resumeSubscription,
  rotateSecret,
} from "../actions";
import { IDLE_ACTION, editPath, subscriptionPath } from "../state";

import { ActionProblem } from "./action-problem";
import { ConfirmDialog } from "./confirm-dialog";
import { SecretPanel } from "./secret-panel";
import { BUTTON, ROW_ACTION_DANGER, ROW_ACTION_LINK } from "./styles";

const STATUS_LABELS: Record<string, string> = {
  pause: "Pause",
  "pause-pending": "Pausing…",
  resume: "Resume",
  "resume-pending": "Resuming…",
};

export function SubscriptionActions({
  id,
  agency,
  agencyName,
  url,
  paused,
  statusButton = true,
}: {
  id: string;
  agency: string;
  agencyName: string;
  url: string;
  paused: boolean;
  statusButton?: boolean;
}) {
  const [statusState, changeStatus, changing] = useActionState(
    paused ? resumeSubscription : pauseSubscription,
    IDLE_ACTION,
  );
  const [rotation, rotate, rotating] = useActionState(
    rotateSecret,
    IDLE_ACTION,
  );
  const [removal, remove, removing] = useActionState(
    deleteSubscription,
    IDLE_ACTION,
  );
  const [hidden, setHidden] = useState<string | null>(null);
  const fields = { id, agency };
  const shownSecret =
    rotation.kind === "rotated" && rotation.secret !== hidden
      ? rotation.secret
      : null;

  return (
    <div className="space-y-3">
      <div className="grid grid-cols-2 gap-2 sm:flex sm:flex-wrap sm:gap-x-5 sm:gap-y-0">
        <Link
          href={subscriptionPath(id)}
          aria-label={`Deliveries and tests for ${agencyName}`}
          className={ROW_ACTION_LINK}
        >
          Deliveries and tests
        </Link>
        <Link
          href={editPath(id)}
          aria-label={`Edit ${agencyName}`}
          className={ROW_ACTION_LINK}
        >
          Edit
        </Link>
        {statusButton ? (
          <form action={changeStatus} className="contents">
            <input type="hidden" name="id" value={id} />
            <button
              type="submit"
              disabled={changing}
              aria-label={`${paused ? "Resume" : "Pause"} ${agencyName}`}
              className={ROW_ACTION_LINK}
            >
              {
                STATUS_LABELS[
                  `${paused ? "resume" : "pause"}${changing ? "-pending" : ""}`
                ]
              }
            </button>
          </form>
        ) : null}
        <ConfirmDialog
          triggerLabel="Rotate secret"
          triggerAccessibleName={`Rotate the secret for ${agencyName}`}
          triggerClassName={ROW_ACTION_LINK}
          title={`Rotate the secret for ${agencyName}?`}
          confirmLabel="Rotate secret"
          pendingLabel="Rotating…"
          cancelLabel="Cancel"
          action={rotate}
          pending={rotating}
          closeWhen={rotation}
          fields={fields}
        >
          <p>
            A new secret replaces the current one straight away. Webhooks signed
            with the new secret will fail your receiver&rsquo;s check until you
            give it the new one.
          </p>
          <p>The new secret is shown once, right after you rotate it.</p>
        </ConfirmDialog>
        <ConfirmDialog
          triggerLabel="Delete"
          triggerAccessibleName={`Delete ${agencyName}`}
          triggerClassName={ROW_ACTION_DANGER}
          title={`Delete the ${agencyName} subscription?`}
          confirmLabel="Delete subscription"
          pendingLabel="Deleting…"
          cancelLabel="Keep it"
          danger
          action={remove}
          pending={removing}
          closeWhen={removal}
          fields={fields}
        >
          <p>
            Webhooks stop at once and its secret is deleted. This cannot be
            undone; to follow {agencyName} again you would add a new
            subscription with a new secret.
          </p>
          <p className="font-mono text-sm break-all text-ink">{url}</p>
        </ConfirmDialog>
      </div>
      <ActionProblem state={statusState} />
      <ActionProblem state={rotation} />
      <ActionProblem state={removal} />
      {shownSecret === null ? null : (
        <SecretPanel
          title={`New secret for ${agencyName}`}
          secret={shownSecret}
          footer={
            <button
              type="button"
              onClick={() => setHidden(shownSecret)}
              className={BUTTON}
            >
              Done, hide the secret
            </button>
          }
        >
          The old secret stopped working just now.
        </SecretPanel>
      )}
    </div>
  );
}
