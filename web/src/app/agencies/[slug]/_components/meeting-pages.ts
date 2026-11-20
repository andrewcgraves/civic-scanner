import { notFound } from "next/navigation";

import type { MeetingList } from "@/lib/api/client";
import { loadAgencyMeetings, type Loaded } from "@/lib/api/load";

import { isUpcomingMeeting } from "./meeting-split";

export const MAX_FIRST_PAGE_REQUESTS = 4;

const CURSOR_PATTERN = /^[A-Za-z0-9_-]{1,512}$/;

export function parseCursor(
  value: string | string[] | undefined,
): string | undefined {
  if (value === undefined) {
    return undefined;
  }
  if (typeof value !== "string" || !CURSOR_PATTERN.test(value)) {
    notFound();
  }
  return value;
}

export function agencyPageHref(slug: string, cursor?: string): string {
  const path = `/agencies/${slug}`;
  return cursor === undefined
    ? path
    : `${path}?${new URLSearchParams({ cursor }).toString()}`;
}

function endsUpcoming(meetings: MeetingList["meetings"], now: Date): boolean {
  const last = meetings.at(-1);
  return last !== undefined && isUpcomingMeeting(last, now);
}

export async function loadMeetingPage(
  slug: string,
  cursor: string | undefined,
  now: Date,
): Promise<Loaded<MeetingList>> {
  const first = await loadAgencyMeetings(slug, cursor);
  if (!first.ok || cursor !== undefined) {
    return first;
  }
  const meetings = [...first.data.meetings];
  let nextCursor = first.data.nextCursor;
  for (
    let requests = 1;
    requests < MAX_FIRST_PAGE_REQUESTS &&
    nextCursor !== null &&
    endsUpcoming(meetings, now);
    requests++
  ) {
    const more = await loadAgencyMeetings(slug, nextCursor);
    if (!more.ok) {
      return more;
    }
    meetings.push(...more.data.meetings);
    nextCursor = more.data.nextCursor;
  }
  return { ok: true, data: { meetings, nextCursor } };
}
