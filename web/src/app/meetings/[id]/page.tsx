import type { Metadata } from "next";

import type { BreadcrumbItem } from "@/components/ui/breadcrumb";
import { MeetingTime } from "@/components/ui/date-time";
import { ExternalLink, isWebUrl } from "@/components/ui/external-link";
import {
  type MeetingDisplayKind,
  MeetingStatusBadge,
  meetingDisplay,
} from "@/components/ui/meeting-status-badge";
import { Notice, UnavailableNotice } from "@/components/ui/notice";
import { PageHeader } from "@/components/ui/page-header";
import type { MeetingDetail } from "@/lib/api/client";
import { loadMeeting } from "@/lib/api/load";
import { formatMeetingDate } from "@/lib/datetime";
import { isFlagEnabled, requireFlag } from "@/lib/flags/server";

import { AgendaSection } from "./_components/agenda-section";
import { TranscriptSection } from "./_components/transcript-section";

const AGENCIES_CRUMB: BreadcrumbItem = { label: "Agencies", href: "/agencies" };

interface MeetingPageProps {
  params: Promise<{ id: string }>;
}

export async function generateMetadata({
  params,
}: MeetingPageProps): Promise<Metadata> {
  await requireFlag("meeting-pages");
  const { id } = await params;
  const meeting = await loadMeeting(id);
  if (!meeting.ok) {
    return { title: "Meeting" };
  }
  const { title, agency, startsAt, status } = meeting.data;
  return {
    title: `${title} — ${agency.name}`,
    description: describeMeeting(
      title,
      agency.name,
      startsAt,
      meetingDisplay(status, startsAt, new Date()).kind,
    ),
  };
}

function describeMeeting(
  title: string,
  agencyName: string,
  startsAt: string,
  kind: MeetingDisplayKind,
): string {
  const date = formatMeetingDate(startsAt);
  if (kind === "cancelled") {
    return `${title}, a meeting of ${agencyName} scheduled for ${date}, has been cancelled.`;
  }
  if (kind === "date-passed") {
    return `${title}, a meeting of ${agencyName} scheduled for ${date}. The date has passed; Civic Scanner has no confirmation that it took place.`;
  }
  return `${title}, a meeting of ${agencyName} on ${date}.`;
}

function breadcrumbFor(
  meeting: MeetingDetail,
  agencyPagesOn: boolean,
): BreadcrumbItem[] {
  return [
    AGENCIES_CRUMB,
    agencyPagesOn
      ? {
          label: meeting.agency.name,
          href: `/agencies/${encodeURIComponent(meeting.agency.slug)}`,
        }
      : { label: meeting.agency.name },
    { label: meeting.title },
  ];
}

export default async function MeetingPage({ params }: MeetingPageProps) {
  await requireFlag("meeting-pages");
  const { id } = await params;
  const [meeting, agencyPagesOn] = await Promise.all([
    loadMeeting(id),
    isFlagEnabled("agency-pages"),
  ]);

  if (!meeting.ok) {
    return (
      <>
        <PageHeader
          title="Meeting"
          breadcrumb={[AGENCIES_CRUMB, { label: "Meeting" }]}
        />
        <UnavailableNotice what="This meeting is" />
      </>
    );
  }

  const data = meeting.data;
  const display = meetingDisplay(data.status, data.startsAt, new Date());
  return (
    <>
      <PageHeader
        title={data.title}
        eyebrow={data.agency.name}
        breadcrumb={breadcrumbFor(data, agencyPagesOn)}
      >
        <MeetingStatusBadge display={display} />
        <MeetingTime value={data.startsAt} />
        {!isWebUrl(data.sourceUrl) ? null : (
          <ExternalLink href={data.sourceUrl}>
            This meeting on {data.agency.name}&apos;s site
          </ExternalLink>
        )}
      </PageHeader>
      {display.kind === "cancelled" ? (
        <div className="mb-10">
          <Notice title="This meeting has been cancelled">
            {data.agency.name} lists this meeting as cancelled. It had been
            scheduled for <MeetingTime value={data.startsAt} />.
          </Notice>
        </div>
      ) : null}
      {display.kind === "date-passed" ? (
        <div className="mb-10">
          <Notice>
            The date of this meeting has passed, and Civic Scanner has no
            confirmation from {data.agency.name}&apos;s site that it took place.
          </Notice>
        </div>
      ) : null}
      <AgendaSection
        agenda={data.agenda}
        agencyName={data.agency.name}
        status={data.status}
      />
      <TranscriptSection meeting={data} display={display.kind} />
    </>
  );
}
