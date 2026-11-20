import { MeetingTime } from "@/components/ui/date-time";
import { ExternalLink } from "@/components/ui/external-link";
import { LinkList, LinkListItem } from "@/components/ui/link-list";
import { MeetingStatusBadge } from "@/components/ui/meeting-status-badge";
import type { MeetingSummary } from "@/lib/api/client";

export function AgencyMeetingList({
  label,
  meetings,
  linkToMeetings,
}: {
  label: string;
  meetings: readonly MeetingSummary[];
  linkToMeetings: boolean;
}) {
  return (
    <LinkList label={label}>
      {meetings.map((meeting) => (
        <LinkListItem
          key={meeting.id}
          href={linkToMeetings ? `/meetings/${meeting.id}` : undefined}
          title={meeting.title}
          meta={<MeetingTime value={meeting.startsAt} />}
          aside={<MeetingStatusBadge status={meeting.status} />}
          description={
            linkToMeetings || meeting.sourceUrl === undefined ? undefined : (
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
