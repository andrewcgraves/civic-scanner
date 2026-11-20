import type { Metadata } from "next";

import type { BreadcrumbItem } from "@/components/ui/breadcrumb";
import { MeetingTime } from "@/components/ui/date-time";
import { ExternalLink } from "@/components/ui/external-link";
import { MeetingStatusBadge } from "@/components/ui/meeting-status-badge";
import { Notice, UnavailableNotice } from "@/components/ui/notice";
import { PageHeader } from "@/components/ui/page-header";
import type { MeetingDetail } from "@/lib/api/client";
import { loadMeeting } from "@/lib/api/load";
import { formatMeetingDate } from "@/lib/datetime";
import { isFlagEnabled, requireFlag } from "@/lib/flags/server";

import { AgendaSection } from "./_components/agenda-section";
import { TranscriptSection } from "./_components/transcript-section";

const SITE_NAME = "Civic Scanner";
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
    return { title: `Meeting | ${SITE_NAME}` };
  }
  const { title, agency, startsAt, status } = meeting.data;
  const date = formatMeetingDate(startsAt);
  return {
    title: `${title} — ${agency.name} | ${SITE_NAME}`,
    description:
      status === "cancelled"
        ? `${title}, a meeting of ${agency.name} scheduled for ${date}, has been cancelled.`
        : `${title}, a meeting of ${agency.name} on ${date}.`,
  };
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
          href: `/agencies/${meeting.agency.slug}`,
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
  return (
    <>
      <PageHeader
        title={data.title}
        eyebrow={data.agency.name}
        breadcrumb={breadcrumbFor(data, agencyPagesOn)}
      >
        <MeetingStatusBadge status={data.status} />
        <MeetingTime value={data.startsAt} />
        {data.sourceUrl === undefined ? null : (
          <ExternalLink href={data.sourceUrl}>
            This meeting on {data.agency.name}&apos;s site
          </ExternalLink>
        )}
      </PageHeader>
      {data.status === "cancelled" ? (
        <div className="mb-10">
          <Notice title="This meeting has been cancelled">
            {data.agency.name} lists this meeting as cancelled. It had been
            scheduled for <MeetingTime value={data.startsAt} />.
          </Notice>
        </div>
      ) : null}
      <AgendaSection agenda={data.agenda} agencyName={data.agency.name} />
      <TranscriptSection meeting={data} />
    </>
  );
}
