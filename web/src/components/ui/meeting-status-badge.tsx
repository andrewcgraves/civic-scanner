import type { MeetingStatus } from "@/lib/api/client";

import { Badge } from "./badge";

export const MEETING_STATUS_LABELS: Record<MeetingStatus, string> = {
  scheduled: "Scheduled",
  held: "Held",
  cancelled: "Cancelled",
};

export function MeetingStatusBadge({ status }: { status: MeetingStatus }) {
  return <Badge tone={status}>{MEETING_STATUS_LABELS[status]}</Badge>;
}
