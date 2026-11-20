import type { MeetingStatus } from "@/lib/api/client";

import { Badge } from "./badge";

export const MEETING_STATUS_LABELS: Record<MeetingStatus, string> = {
  scheduled: "Scheduled",
  held: "Held",
  cancelled: "Cancelled",
};

export const UNKNOWN_STATUS_LABEL = "Status unknown";

const READABLE_STATUS = /^[a-z][a-z_]{0,29}$/;

function isKnownStatus(status: string): status is MeetingStatus {
  return Object.hasOwn(MEETING_STATUS_LABELS, status);
}

export function meetingStatusLabel(status: string): string {
  if (isKnownStatus(status)) {
    return MEETING_STATUS_LABELS[status];
  }
  if (!READABLE_STATUS.test(status)) {
    return UNKNOWN_STATUS_LABEL;
  }
  const words = status.replaceAll("_", " ");
  return words.charAt(0).toUpperCase() + words.slice(1);
}

export function MeetingStatusBadge({ status }: { status: string }) {
  return (
    <Badge tone={isKnownStatus(status) ? status : "neutral"}>
      {meetingStatusLabel(status)}
    </Badge>
  );
}
