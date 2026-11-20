import Link from "next/link";

import { EmptyState } from "@/components/ui/empty-state";
import { UnavailableNotice } from "@/components/ui/notice";
import { Section } from "@/components/ui/section";
import type { MeetingList } from "@/lib/api/client";
import type { Loaded } from "@/lib/api/load";

import { AgencyMeetingList } from "./agency-meeting-list";
import { agencyPageHref } from "./meeting-pages";
import { splitMeetings } from "./meeting-split";

function MeetingPagination({
  slug,
  laterPage,
  nextCursor,
}: {
  slug: string;
  laterPage: boolean;
  nextCursor: string | null;
}) {
  if (!laterPage && nextCursor === null) {
    return null;
  }
  return (
    <nav
      aria-label="Meeting pages"
      className="mt-6 flex flex-wrap justify-between gap-4 text-sm"
    >
      {laterPage ? (
        <Link href={agencyPageHref(slug)} className="link">
          <span aria-hidden="true">← </span>Newest meetings
        </Link>
      ) : (
        <span />
      )}
      {nextCursor === null ? null : (
        <Link href={agencyPageHref(slug, nextCursor)} className="link">
          Older meetings<span aria-hidden="true"> →</span>
        </Link>
      )}
    </nav>
  );
}

export function AgencyMeetings({
  slug,
  page,
  laterPage,
  now,
  linkToMeetings,
}: {
  slug: string;
  page: Loaded<MeetingList>;
  laterPage: boolean;
  now: Date;
  linkToMeetings: boolean;
}) {
  if (!page.ok) {
    return (
      <Section title="Meetings">
        <UnavailableNotice what="Meetings are" />
      </Section>
    );
  }

  const { meetings, nextCursor } = page.data;
  if (!laterPage && meetings.length === 0) {
    return (
      <Section title="Meetings">
        <EmptyState title="Meetings for this agency are not tracked yet.">
          Civic Scanner has no meetings on record for this agency so far. The
          agency&apos;s own website lists its meeting schedule.
        </EmptyState>
      </Section>
    );
  }

  const { upcoming, past } = splitMeetings(meetings, now);
  return (
    <>
      {laterPage && upcoming.length === 0 ? null : (
        <Section
          title="Upcoming meetings"
          description={
            laterPage ? "Continued from the newer pages." : "Soonest first."
          }
        >
          {upcoming.length === 0 ? (
            <EmptyState title="No upcoming meetings are posted.">
              Civic Scanner has no future meetings on record for this agency
              right now.
            </EmptyState>
          ) : (
            <AgencyMeetingList
              label="Upcoming meetings"
              meetings={upcoming}
              linkToMeetings={linkToMeetings}
            />
          )}
        </Section>
      )}
      <Section
        title={laterPage ? "Older meetings" : "Past meetings"}
        description="Most recent first."
      >
        {past.length === 0 ? (
          <EmptyState
            title={
              laterPage
                ? "There are no older meetings."
                : "No past meetings are on record yet."
            }
          />
        ) : (
          <AgencyMeetingList
            label={laterPage ? "Older meetings" : "Past meetings"}
            meetings={past}
            linkToMeetings={linkToMeetings}
          />
        )}
        <MeetingPagination
          slug={slug}
          laterPage={laterPage}
          nextCursor={nextCursor}
        />
      </Section>
    </>
  );
}
