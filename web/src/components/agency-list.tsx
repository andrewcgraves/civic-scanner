import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ExternalLink, isWebUrl } from "@/components/ui/external-link";
import { LinkList, LinkListItem } from "@/components/ui/link-list";
import type { Agency } from "@/lib/api/client";

const LIST_LABEL = "Agencies";

export const COMING_SOON_LABEL = "Coming soon";

export const COMING_SOON_NOTE = `Agencies marked "${COMING_SOON_LABEL}" are not connected yet: Civic Scanner has no meetings for them so far.`;

function hostnameOf(website: string): string | null {
  return isWebUrl(website)
    ? new URL(website).hostname.replace(/^www\./, "")
    : null;
}

function AgencyRows({
  agencies,
  linkToAgencyPages,
  comingSoon,
}: {
  agencies: Agency[];
  linkToAgencyPages: boolean;
  comingSoon: ReadonlySet<string>;
}) {
  return (
    <LinkList label={LIST_LABEL}>
      {agencies.map((agency) => {
        const hostname = hostnameOf(agency.website);
        const aside = comingSoon.has(agency.id) ? (
          <Badge>{COMING_SOON_LABEL}</Badge>
        ) : undefined;
        if (linkToAgencyPages) {
          return (
            <LinkListItem
              key={agency.id}
              href={`/agencies/${encodeURIComponent(agency.slug)}`}
              title={agency.name}
              meta={hostname ?? undefined}
              aside={aside}
            />
          );
        }
        return (
          <LinkListItem
            key={agency.id}
            title={agency.name}
            aside={aside}
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
  comingSoon = new Set(),
}: {
  agencies: Agency[];
  linkToAgencyPages: boolean;
  comingSoon?: ReadonlySet<string>;
}) {
  if (agencies.length === 0) {
    return <EmptyState title="No agencies are covered yet." />;
  }
  const anyComingSoon = agencies.some((agency) => comingSoon.has(agency.id));
  return (
    <>
      {anyComingSoon ? (
        <p className="mb-4 max-w-[65ch] text-ink-muted">{COMING_SOON_NOTE}</p>
      ) : null}
      <AgencyRows
        agencies={agencies}
        linkToAgencyPages={linkToAgencyPages}
        comingSoon={comingSoon}
      />
    </>
  );
}
