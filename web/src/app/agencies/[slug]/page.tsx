import type { Metadata } from "next";

import { ExternalLink } from "@/components/ui/external-link";
import { UnavailableNotice } from "@/components/ui/notice";
import { PageHeader } from "@/components/ui/page-header";
import { loadAgency } from "@/lib/api/load";
import { isFlagEnabled, requireFlag } from "@/lib/flags/server";

import { AgencyMeetings } from "./_components/agency-meetings";
import { loadMeetingPage, parseCursor } from "./_components/meeting-pages";

interface AgencyPageProps {
  params: Promise<{ slug: string }>;
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}

const SITE_NAME = "Civic Scanner";

export async function generateMetadata({
  params,
}: Pick<AgencyPageProps, "params">): Promise<Metadata> {
  await requireFlag("agency-pages");
  const { slug } = await params;
  const agency = await loadAgency(slug);
  if (!agency.ok) {
    return { title: `Agency | ${SITE_NAME}` };
  }
  const { name } = agency.data;
  return {
    title: `${name} | ${SITE_NAME}`,
    description: `Upcoming and past public meetings of ${name}, tracked by ${SITE_NAME}.`,
  };
}

export default async function AgencyPage({
  params,
  searchParams,
}: AgencyPageProps) {
  await requireFlag("agency-pages");
  const { slug } = await params;
  const cursor = parseCursor((await searchParams).cursor);
  const now = new Date();

  const [agency, page, linkToMeetings] = await Promise.all([
    loadAgency(slug),
    loadMeetingPage(slug, cursor, now),
    isFlagEnabled("meeting-pages"),
  ]);

  const name = agency.ok ? agency.data.name : "Agency";

  return (
    <>
      <PageHeader
        title={name}
        breadcrumb={[{ label: "Agencies", href: "/agencies" }, { label: name }]}
      >
        {agency.ok ? (
          <ExternalLink href={agency.data.website}>Agency website</ExternalLink>
        ) : undefined}
      </PageHeader>
      {agency.ok ? null : (
        <div className="mb-10">
          <UnavailableNotice what="This agency's details are" />
        </div>
      )}
      <AgencyMeetings
        slug={slug}
        page={page}
        laterPage={cursor !== undefined}
        now={now}
        linkToMeetings={linkToMeetings}
      />
    </>
  );
}
