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

function LinkedAgencies({ agencies }: { agencies: Agency[] }) {
  return (
    <LinkList label={LIST_LABEL}>
      {agencies.map((agency) => (
        <LinkListItem
          key={agency.id}
          href={`/agencies/${agency.slug}`}
          title={agency.name}
          meta={hostnameOf(agency.website) ?? undefined}
        />
      ))}
    </LinkList>
  );
}

function PlainAgencies({ agencies }: { agencies: Agency[] }) {
  return (
    <ul
      aria-label={LIST_LABEL}
      className="divide-y divide-line overflow-hidden rounded-lg border border-line bg-surface"
    >
      {agencies.map((agency) => {
        const hostname = hostnameOf(agency.website);
        return (
          <li key={agency.id} className="px-4 py-4 sm:px-5">
            <span className="block font-medium">{agency.name}</span>
            {hostname === null ? null : (
              <span className="mt-1 block text-sm">
                <ExternalLink href={agency.website}>{hostname}</ExternalLink>
              </span>
            )}
          </li>
        );
      })}
    </ul>
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
  return linkToAgencyPages ? (
    <LinkedAgencies agencies={agencies} />
  ) : (
    <PlainAgencies agencies={agencies} />
  );
}
