import type { Agency } from "@/lib/api/client";

function hostnameOf(website: string): string | null {
  try {
    return new URL(website).hostname.replace(/^www\./, "");
  } catch {
    return null;
  }
}

export function AgencyList({ agencies }: { agencies: Agency[] }) {
  if (agencies.length === 0) {
    return <p>No agencies are tracked yet.</p>;
  }

  return (
    <ul className="list-disc pl-5">
      {agencies.map((agency) => {
        const hostname = hostnameOf(agency.website);
        return (
          <li key={agency.id}>
            {agency.name}
            {hostname === null ? null : (
              <>
                {" "}
                <a href={agency.website} className="underline">
                  {hostname}
                </a>
              </>
            )}
          </li>
        );
      })}
    </ul>
  );
}
