import type { MeetingSummary } from "@/lib/api/client";
import { isUpcoming } from "@/lib/datetime";

export interface SplitMeetings {
  upcoming: MeetingSummary[];
  past: MeetingSummary[];
}

export function isUpcomingMeeting(meeting: MeetingSummary, now: Date): boolean {
  return meeting.status !== "held" && isUpcoming(meeting.startsAt, now);
}

export function splitMeetings(
  newestFirst: readonly MeetingSummary[],
  now: Date,
): SplitMeetings {
  const upcoming: MeetingSummary[] = [];
  const past: MeetingSummary[] = [];
  for (const meeting of newestFirst) {
    if (isUpcomingMeeting(meeting, now)) {
      upcoming.unshift(meeting);
    } else {
      past.push(meeting);
    }
  }
  return { upcoming, past };
}
