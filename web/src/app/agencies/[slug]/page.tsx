import type { Metadata } from "next";

import { ExternalLink, isWebUrl } from "@/components/ui/external-link";
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
const RSS_TYPE = "application/rss+xml";

function feedHref(slug: string): string {
  return `/agencies/${encodeURIComponent(slug)}/feed.xml`;
}

export async function generateMetadata({
  params,
}: Pick<AgencyPageProps, "params">): Promise<Metadata> {
  await requireFlag("agency-pages");
  const { slug } = await params;
  const [agency, feedOn] = await Promise.all([
    loadAgency(slug),
    isFlagEnabled("agency-rss-feeds"),
  ]);
  const name = agency.ok ? agency.data.name : undefined;
  const alternates: Metadata["alternates"] = feedOn
    ? {
        types: {
          [RSS_TYPE]: [
            {
              url: feedHref(slug),
              title: `${name ?? "Agency"} meetings`,
            },
          ],
        },
      }
    : undefined;
  if (name === undefined) {
    return { title: "Agency", alternates };
  }
  return {
    title: name,
    description: `Upcoming and past public meetings of ${name}, tracked by ${SITE_NAME}.`,
    alternates,
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

  const [agency, page, linkToMeetings, feedOn] = await Promise.all([
    loadAgency(slug),
    loadMeetingPage(slug, cursor, now),
    isFlagEnabled("meeting-pages"),
    isFlagEnabled("agency-rss-feeds"),
  ]);

  const name = agency.ok ? agency.data.name : "Agency";
  const website =
    agency.ok && isWebUrl(agency.data.website)
      ? agency.data.website
      : undefined;

  return (
    <>
      <PageHeader
        title={name}
        breadcrumb={[{ label: "Agencies", href: "/agencies" }, { label: name }]}
      >
        {website !== undefined || feedOn ? (
          <>
            {website === undefined ? null : (
              <ExternalLink href={website}>Agency website</ExternalLink>
            )}
            {feedOn ? (
              <a href={feedHref(slug)} type={RSS_TYPE} className="link">
                RSS feed
              </a>
            ) : null}
          </>
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
