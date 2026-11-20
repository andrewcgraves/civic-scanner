import type { Metadata } from "next";

import { AgencyList } from "@/components/agency-list";
import { UnavailableNotice } from "@/components/ui/notice";
import { PageHeader } from "@/components/ui/page-header";
import { Section } from "@/components/ui/section";
import { findComingSoon } from "@/lib/api/coming-soon";
import { loadAgencies } from "@/lib/api/load";
import { isFlagEnabled } from "@/lib/flags/server";

export const metadata: Metadata = {
  description:
    "Civic Scanner follows the board meetings of transportation agencies: their agendas, short AI-written summaries and what changed when an agenda is revised, always with a link to the agency's own documents.",
};

const WHAT_IT_DOES = [
  "Every day, it checks each agency's website for board meetings, upcoming and past.",
  "Collects each meeting's agenda and links to the agency's own copy.",
  "Writes a short summary of each agenda with AI, so you can see what a meeting will cover at a glance.",
  "Notes what changed when an agency revises an agenda after first publishing it.",
  "Transcripts of meeting recordings are on the way.",
] as const;

const GOOD_TO_KNOW = [
  "The agency's own documents are the official record. Wherever we show an agenda or a summary, we link to the agency's original.",
  "Summaries are written by AI. They can be wrong or leave out something important, so check the original agenda before relying on one.",
  "We cover only the agencies listed below. Meetings of other agencies, or other meetings of these agencies, are not here.",
  "Civic Scanner is independent and is not run by any of these agencies.",
] as const;

function PlainList({ items }: { items: readonly string[] }) {
  return (
    <ul className="max-w-[65ch] list-disc space-y-2 pl-5">
      {items.map((item) => (
        <li key={item}>{item}</li>
      ))}
    </ul>
  );
}

export default async function HomePage() {
  const [agencies, linkToAgencyPages] = await Promise.all([
    loadAgencies(),
    isFlagEnabled("agency-pages"),
  ]);
  const comingSoon = agencies.ok
    ? await findComingSoon(agencies.data)
    : new Set<string>();

  return (
    <>
      <PageHeader
        title="Follow your transportation agency's board meetings"
        description="Transportation agencies decide on fares, service and spending at public board meetings. Civic Scanner gathers those meetings' agendas in one place and explains them in plain language."
      />
      <Section title="What Civic Scanner does">
        <PlainList items={WHAT_IT_DOES} />
      </Section>
      <Section title="Good to know">
        <PlainList items={GOOD_TO_KNOW} />
      </Section>
      <Section title="Agencies we cover">
        {agencies.ok ? (
          <AgencyList
            agencies={agencies.data}
            linkToAgencyPages={linkToAgencyPages}
            comingSoon={comingSoon}
          />
        ) : (
          <UnavailableNotice what="Agencies are" />
        )}
      </Section>
    </>
  );
}
