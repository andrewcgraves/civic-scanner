"use client";

import Link from "next/link";
import { useActionState, useState } from "react";

import { Notice } from "@/components/ui/notice";

import {
  deleteSubscription,
  pauseSubscription,
  resumeSubscription,
  rotateSecret,
} from "../actions";
import { IDLE_ACTION, editPath, type ActionState } from "../state";

import { ConfirmDialog } from "./confirm-dialog";
import { SecretPanel } from "./secret-panel";
import { BUTTON, ROW_ACTION_DANGER, ROW_ACTION_LINK } from "./styles";

const STATUS_LABELS: Record<string, string> = {
  pause: "Pause",
  "pause-pending": "Pausing…",
  resume: "Resume",
  "resume-pending": "Resuming…",
};

function Problem({ state }: { state: ActionState }) {
  switch (state.kind) {
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
          Subscriptions need the paid plan. Start it again to change this one.
        </Notice>
      );
    case "unavailable":
      return (
        <Notice tone="error">
          That could not be done right now. Please try again later.
        </Notice>
      );
    default:
      return null;
  }
}

export function SubscriptionActions({
  id,
  agency,
  agencyName,
  url,
  paused,
}: {
  id: string;
  agency: string;
  agencyName: string;
  url: string;
  paused: boolean;
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
          href={editPath(id)}
          aria-label={`Edit ${agencyName}`}
          className={ROW_ACTION_LINK}
        >
          Edit
        </Link>
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
      <Problem state={statusState} />
      <Problem state={rotation} />
      <Problem state={removal} />
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
