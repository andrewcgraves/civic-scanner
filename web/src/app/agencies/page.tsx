import type { Metadata } from "next";

import { AgencyList } from "@/components/agency-list";
import { UnavailableNotice } from "@/components/ui/notice";
import { PageHeader } from "@/components/ui/page-header";
import { findComingSoon } from "@/lib/api/coming-soon";
import { loadAgencies } from "@/lib/api/load";
import { hasPublicRedirectUri } from "@/lib/auth/config";
import { isFlagEnabled } from "@/lib/flags/server";

export const metadata: Metadata = {
  title: "Agencies",
  description:
    "The transportation agencies whose board meetings Civic Scanner follows.",
};

export default async function AgenciesPage() {
  const [agencies, linkToAgencyPages, accounts, submissions] =
    await Promise.all([
      loadAgencies(),
      isFlagEnabled("agency-pages"),
      isFlagEnabled("accounts"),
      isFlagEnabled("agency-submissions"),
    ]);
  const canSuggest = hasPublicRedirectUri() && accounts && submissions;
  const comingSoon = agencies.ok
    ? await findComingSoon(agencies.data)
    : new Set<string>();

  return (
    <>
      <PageHeader
        title="Agencies"
        description="The transportation agencies whose board meetings Civic Scanner follows."
      >
        {canSuggest ? (
          <a href="/submit-agency" className="link">
            Suggest an agency
          </a>
        ) : undefined}
      </PageHeader>
      {agencies.ok ? (
        <AgencyList
          agencies={agencies.data}
          linkToAgencyPages={linkToAgencyPages}
          comingSoon={comingSoon}
        />
      ) : (
        <UnavailableNotice what="Agencies are" />
      )}
    </>
  );
}
