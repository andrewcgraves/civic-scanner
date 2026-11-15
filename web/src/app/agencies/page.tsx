import type { Metadata } from "next";

import { AgencyList } from "@/components/agency-list";
import { api } from "@/lib/api/client";
import type { Agency } from "@/lib/api/client";

export const metadata: Metadata = { title: "Agencies | Civic Scanner" };

export const dynamic = "force-dynamic";

async function loadAgencies(): Promise<Agency[] | null> {
  try {
    const { data } = await api.GET("/agencies");
    return data?.agencies ?? null;
  } catch {
    return null;
  }
}

export default async function AgenciesPage() {
  const agencies = await loadAgencies();

  return (
    <main>
      <h1 className="text-2xl font-semibold">Agencies</h1>
      <div className="mt-4">
        {agencies === null ? (
          <p role="alert">
            Agencies are unavailable right now. Please try again later.
          </p>
        ) : (
          <AgencyList agencies={agencies} />
        )}
      </div>
    </main>
  );
}
