import { MeetingTime } from "@/components/ui/date-time";
import { ExternalLink, isWebUrl } from "@/components/ui/external-link";
import { LinkList, LinkListItem } from "@/components/ui/link-list";
import {
  MeetingStatusBadge,
  meetingDisplay,
} from "@/components/ui/meeting-status-badge";
import type { MeetingSummary } from "@/lib/api/client";

export function AgencyMeetingList({
  label,
  meetings,
  linkToMeetings,
  now,
}: {
  label: string;
  meetings: readonly MeetingSummary[];
  linkToMeetings: boolean;
  now: Date;
}) {
  return (
    <LinkList label={label}>
      {meetings.map((meeting) => (
        <LinkListItem
          key={meeting.id}
          href={
            linkToMeetings
              ? `/meetings/${encodeURIComponent(meeting.id)}`
              : undefined
          }
          title={meeting.title}
          meta={<MeetingTime value={meeting.startsAt} />}
          aside={
            <MeetingStatusBadge
              display={meetingDisplay(meeting.status, meeting.startsAt, now)}
            />
          }
          description={
            linkToMeetings || !isWebUrl(meeting.sourceUrl) ? undefined : (
              <ExternalLink href={meeting.sourceUrl}>
                Meeting page on the agency&apos;s site
              </ExternalLink>
            )
          }
        />
      ))}
    </LinkList>
  );
}
