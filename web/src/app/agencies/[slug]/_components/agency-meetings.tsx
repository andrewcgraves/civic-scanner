import Link from "next/link";

import { EmptyState } from "@/components/ui/empty-state";
import { Notice, UnavailableNotice } from "@/components/ui/notice";
import { Section } from "@/components/ui/section";
import type { Loaded } from "@/lib/api/load";

import { AgencyMeetingList } from "./agency-meeting-list";
import { agencyPageHref, type MeetingPage } from "./meeting-pages";
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

function upcomingDescription(laterPage: boolean, moreUpcoming: boolean) {
  if (moreUpcoming) {
    return "Soonest first among those shown. More upcoming meetings, including sooner ones, continue on the next page.";
  }
  return laterPage ? "Continued from the newer pages." : "Soonest first.";
}

export function AgencyMeetings({
  slug,
  page,
  laterPage,
  now,
  linkToMeetings,
}: {
  slug: string;
  page: Loaded<MeetingPage>;
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

  const { meetings, nextCursor, incomplete, moreUpcoming } = page.data;
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
  const pastTitle = laterPage ? "Older meetings" : "Past meetings";
  return (
    <>
      {incomplete ? (
        <div className="mb-10">
          <Notice tone="error">
            Some meetings could not be loaded, so this list may be incomplete.
            Please try again later.
          </Notice>
        </div>
      ) : null}
      {laterPage && upcoming.length === 0 ? null : (
        <Section
          title="Upcoming meetings"
          description={upcomingDescription(laterPage, moreUpcoming)}
        >
          {upcoming.length === 0 ? (
            <EmptyState title="No upcoming meetings on record.">
              Civic Scanner has no future meetings on record for this agency
              right now.
            </EmptyState>
          ) : (
            <AgencyMeetingList
              label="Upcoming meetings"
              meetings={upcoming}
              linkToMeetings={linkToMeetings}
              now={now}
            />
          )}
        </Section>
      )}
      {past.length === 0 && nextCursor !== null ? null : (
        <Section title={pastTitle} description="Most recent first.">
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
              label={pastTitle}
              meetings={past}
              linkToMeetings={linkToMeetings}
              now={now}
            />
          )}
        </Section>
      )}
      <MeetingPagination
        slug={slug}
        laterPage={laterPage}
        nextCursor={nextCursor}
      />
    </>
  );
}
