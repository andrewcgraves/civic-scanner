import type { Metadata } from "next";

import { AgencyList } from "@/components/agency-list";
import { UnavailableNotice } from "@/components/ui/notice";
import { PageHeader } from "@/components/ui/page-header";
import { loadAgencies } from "@/lib/api/load";
import { isFlagEnabled } from "@/lib/flags/server";

export const metadata: Metadata = {
  title: "Agencies | Civic Scanner",
  description:
    "The public transit agencies whose board meetings Civic Scanner follows.",
};

export default async function AgenciesPage() {
  const [agencies, linkToAgencyPages] = await Promise.all([
    loadAgencies(),
    isFlagEnabled("agency-pages"),
  ]);

  return (
    <>
      <PageHeader
        title="Agencies"
        description="The public transit agencies whose board meetings Civic Scanner follows."
      />
      {agencies.ok ? (
        <AgencyList
          agencies={agencies.data}
          linkToAgencyPages={linkToAgencyPages}
        />
      ) : (
        <UnavailableNotice what="Agencies are" />
      )}
    </>
  );
}
