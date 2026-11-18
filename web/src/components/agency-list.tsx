import type { Agency } from "@/lib/api/client";

export function AgencyList({ agencies }: { agencies: Agency[] }) {
  if (agencies.length === 0) {
    return <p>No agencies are tracked yet.</p>;
  }

  return (
    <ul className="list-disc pl-5">
      {agencies.map((agency) => (
        <li key={agency.id}>
          {agency.name}{" "}
          <a href={agency.website} className="underline">
            {new URL(agency.website).hostname.replace(/^www\./, "")}
          </a>
        </li>
      ))}
    </ul>
  );
}
