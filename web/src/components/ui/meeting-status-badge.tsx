import type { MeetingStatus } from "@/lib/api/client";
import { isUpcoming } from "@/lib/datetime";

import { Badge, type BadgeTone } from "./badge";

export const MEETING_STATUS_LABELS: Record<MeetingStatus, string> = {
  scheduled: "Scheduled",
  held: "Held",
  cancelled: "Cancelled",
};

export const DATE_PASSED_LABEL = "Date passed";

export const UNKNOWN_STATUS_LABEL = "Status unknown";

const READABLE_STATUS = /^[a-z][a-z_]{0,29}$/;

export type MeetingDisplayKind = MeetingStatus | "date-passed" | "other";

export interface MeetingDisplay {
  kind: MeetingDisplayKind;
  label: string;
  tone: BadgeTone;
}

function isKnownStatus(status: string): status is MeetingStatus {
  return Object.hasOwn(MEETING_STATUS_LABELS, status);
}

function otherStatusLabel(status: string): string {
  if (!READABLE_STATUS.test(status)) {
    return UNKNOWN_STATUS_LABEL;
  }
  const words = status.replaceAll("_", " ");
  return words.charAt(0).toUpperCase() + words.slice(1);
}

export function meetingDisplay(
  status: string,
  startsAt: string,
  now: Date,
): MeetingDisplay {
  if (status === "scheduled" && !isUpcoming(startsAt, now)) {
    return Number.isNaN(Date.parse(startsAt))
      ? { kind: "other", label: UNKNOWN_STATUS_LABEL, tone: "neutral" }
      : { kind: "date-passed", label: DATE_PASSED_LABEL, tone: "neutral" };
  }
  if (isKnownStatus(status)) {
    return { kind: status, label: MEETING_STATUS_LABELS[status], tone: status };
  }
  return { kind: "other", label: otherStatusLabel(status), tone: "neutral" };
}

export function MeetingStatusBadge({ display }: { display: MeetingDisplay }) {
  return <Badge tone={display.tone}>{display.label}</Badge>;
}
