import { EmptyState } from "@/components/ui/empty-state";
import { ExternalLink, isWebUrl } from "@/components/ui/external-link";
import { LinkList, LinkListItem } from "@/components/ui/link-list";
import type { Agency } from "@/lib/api/client";

const LIST_LABEL = "Agencies";

function hostnameOf(website: string): string | null {
  return isWebUrl(website)
    ? new URL(website).hostname.replace(/^www\./, "")
    : null;
}

function AgencyRows({
  agencies,
  linkToAgencyPages,
}: {
  agencies: Agency[];
  linkToAgencyPages: boolean;
}) {
  return (
    <LinkList label={LIST_LABEL}>
      {agencies.map((agency) => {
        const hostname = hostnameOf(agency.website);
        if (linkToAgencyPages) {
          return (
            <LinkListItem
              key={agency.id}
              href={`/agencies/${encodeURIComponent(agency.slug)}`}
              title={agency.name}
              meta={hostname ?? undefined}
            />
          );
        }
        return (
          <LinkListItem
            key={agency.id}
            title={agency.name}
            meta={
              hostname === null ? undefined : (
                <ExternalLink href={agency.website}>{hostname}</ExternalLink>
              )
            }
          />
        );
      })}
    </LinkList>
  );
}

export function AgencyList({
  agencies,
  linkToAgencyPages,
}: {
  agencies: Agency[];
  linkToAgencyPages: boolean;
}) {
  if (agencies.length === 0) {
    return <EmptyState title="No agencies are covered yet." />;
  }
  return (
    <AgencyRows agencies={agencies} linkToAgencyPages={linkToAgencyPages} />
  );
}
