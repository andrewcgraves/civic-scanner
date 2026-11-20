import { MeetingTime } from "@/components/ui/date-time";
import { ExternalLink } from "@/components/ui/external-link";
import { LinkList, LinkListItem } from "@/components/ui/link-list";
import { MeetingStatusBadge } from "@/components/ui/meeting-status-badge";
import type { MeetingSummary } from "@/lib/api/client";

function PlainMeetingItem({ meeting }: { meeting: MeetingSummary }) {
  return (
    <li className="px-4 py-4 sm:px-5">
      <span className="flex items-start justify-between gap-4">
        <span className="font-medium">{meeting.title}</span>
        <span className="shrink-0">
          <MeetingStatusBadge status={meeting.status} />
        </span>
      </span>
      <span className="mt-1 block text-sm text-ink-muted">
        <MeetingTime value={meeting.startsAt} />
      </span>
      {meeting.sourceUrl === undefined ? null : (
        <span className="mt-2 block text-sm">
          <ExternalLink href={meeting.sourceUrl}>
            Meeting page on the agency&apos;s site
          </ExternalLink>
        </span>
      )}
    </li>
  );
}

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
      {meetings.map((meeting) =>
        linkToMeetings ? (
          <LinkListItem
            key={meeting.id}
            href={`/meetings/${meeting.id}`}
            title={meeting.title}
            meta={<MeetingTime value={meeting.startsAt} />}
            aside={<MeetingStatusBadge status={meeting.status} />}
          />
        ) : (
          <PlainMeetingItem key={meeting.id} meeting={meeting} />
        ),
      )}
    </LinkList>
  );
}
