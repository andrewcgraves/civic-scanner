import "server-only";

import { notFound } from "next/navigation";
import { connection } from "next/server";
import { cache } from "react";

import { api } from "./client";
import type { Agency, MeetingDetail, MeetingList } from "./client";

export type Loaded<T> = { ok: true; data: T } | { ok: false };

export const REQUEST_TIMEOUT_MS = 10_000;

const SLUG_PATTERN = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;
const MAX_SLUG_LENGTH = 100;
const UUID_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

interface RequestInitOptions {
  cache: RequestCache;
  signal: AbortSignal;
}

interface ApiResult<T> {
  data?: T;
  response: Response;
}

async function settle<T>(
  label: string,
  call: (init: RequestInitOptions) => Promise<ApiResult<T>>,
): Promise<Loaded<T>> {
  await connection();
  let result: ApiResult<T>;
  try {
    result = await call({
      cache: "no-store",
      signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
    });
  } catch (error) {
    console.error(`API request failed: ${label}`, error);
    return { ok: false };
  }
  const { data, response } = result;
  if (response.status === 404 || response.status === 400) {
    notFound();
  }
  if (!response.ok || data === undefined) {
    console.error(`API request failed: ${label}`, response.status);
    return { ok: false };
  }
  return { ok: true, data };
}

function mapLoaded<T, U>(loaded: Loaded<T>, pick: (data: T) => U): Loaded<U> {
  return loaded.ok ? { ok: true, data: pick(loaded.data) } : loaded;
}

function requireSlug(slug: string): void {
  if (slug.length > MAX_SLUG_LENGTH || !SLUG_PATTERN.test(slug)) {
    notFound();
  }
}

export const loadAgencies = cache(async (): Promise<Loaded<Agency[]>> =>
  mapLoaded(
    await settle("GET /agencies", (init) => api.GET("/agencies", { ...init })),
    (data) => data.agencies,
  ),
);

export const loadAgency = cache(
  async (slug: string): Promise<Loaded<Agency>> => {
    requireSlug(slug);
    return settle("GET /agencies/{slug}", (init) =>
      api.GET("/agencies/{slug}", { ...init, params: { path: { slug } } }),
    );
  },
);

export const loadAgencyMeetings = cache(
  async (
    slug: string,
    cursor?: string,
    limit?: number,
  ): Promise<Loaded<MeetingList>> => {
    requireSlug(slug);
    return settle("GET /agencies/{slug}/meetings", (init) =>
      api.GET("/agencies/{slug}/meetings", {
        ...init,
        params: { path: { slug }, query: { cursor, limit } },
      }),
    );
  },
);

export const loadMeeting = cache(
  async (id: string): Promise<Loaded<MeetingDetail>> => {
    if (!UUID_PATTERN.test(id)) {
      notFound();
    }
    return settle("GET /meetings/{id}", (init) =>
      api.GET("/meetings/{id}", { ...init, params: { path: { id } } }),
    );
  },
);
