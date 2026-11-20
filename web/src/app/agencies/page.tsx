import type { Metadata } from "next";

import { AgencyList } from "@/components/agency-list";
import { PageHeader } from "@/components/ui/page-header";
import { UnavailableNotice } from "@/components/ui/notice";
import { loadAgencies } from "@/lib/api/load";

export const metadata: Metadata = { title: "Agencies | Civic Scanner" };

export default async function AgenciesPage() {
  const agencies = await loadAgencies();

  return (
    <>
      <PageHeader title="Agencies" />
      {agencies.ok ? (
        <AgencyList agencies={agencies.data} />
      ) : (
        <UnavailableNotice what="Agencies are" />
      )}
    </>
  );
}
