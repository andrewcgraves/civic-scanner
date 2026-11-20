import "server-only";

import type { Agency } from "./client";
import { loadAgencyMeetings, REQUEST_TIMEOUT_MS } from "./load";

export const MAX_COMING_SOON_LOOKUPS = 25;

async function hasNoMeetings(
  slug: string,
  deadline: AbortSignal,
): Promise<boolean> {
  try {
    const page = await loadAgencyMeetings(slug, undefined, 1, deadline);
    return page.ok && page.data.meetings.length === 0;
  } catch {
    return false;
  }
}

export async function findComingSoon(
  agencies: readonly Agency[],
  deadline: AbortSignal = AbortSignal.timeout(REQUEST_TIMEOUT_MS),
): Promise<ReadonlySet<string>> {
  if (agencies.length > MAX_COMING_SOON_LOOKUPS) {
    return new Set();
  }
  const empty = await Promise.all(
    agencies.map((agency) => hasNoMeetings(agency.slug, deadline)),
  );
  return new Set(agencies.filter((_, index) => empty[index]).map((a) => a.id));
}
