import "server-only";

import {
  TokenRefreshError,
  refreshSession,
  withAuth,
} from "@workos-inc/authkit-nextjs";
import { cookies, headers } from "next/headers";
import createClient, { type Client } from "openapi-fetch";

import { apiBaseUrl } from "@/lib/api/client";
import type { paths } from "@/lib/api/v1";

import { SESSION_STATE_HEADER, sessionCookieName } from "./cookie";

export type UserApiClient = Client<paths>;

export type UserApiResult<T> =
  | { status: "ok"; data: T }
  | { status: "signed-out" }
  | { status: "unavailable" }
  | { status: "not-found" }
  | { status: "forbidden" }
  | { status: "conflict"; error: unknown }
  | { status: "rejected"; httpStatus: number; error: unknown }
  | { status: "failed" };

export type UserApiCall<T> = (
  client: UserApiClient,
) => Promise<{ data?: T; error?: unknown; response: Response }>;

const REJECTED_STATUSES = new Set([400, 413, 415, 422, 429]);

const API_TIMEOUT_MS = 10_000;

const apiBase = new URL(apiBaseUrl);
const apiPathPrefix = apiBase.pathname.endsWith("/")
  ? apiBase.pathname
  : `${apiBase.pathname}/`;

export function isApiUrl(url: string): boolean {
  const target = new URL(url);
  return (
    target.origin === apiBase.origin &&
    target.username === "" &&
    target.password === "" &&
    `${target.pathname}/`.startsWith(apiPathPrefix)
  );
}

export function userApiClient(accessToken: string): UserApiClient {
  return createClient<paths>({
    baseUrl: apiBaseUrl,
    fetch: (request) => {
      if (!isApiUrl(request.url)) {
        return Promise.reject(
          new Error("The access token is only sent to the API base URL"),
        );
      }
      const headers = new Headers(request.headers);
      headers.set("authorization", `Bearer ${accessToken}`);
      return fetch(request, {
        headers,
        redirect: "manual",
        cache: "no-store",
        signal: AbortSignal.timeout(API_TIMEOUT_MS),
      });
    },
  });
}

type Attempt<T> = UserApiResult<T> | { status: "unauthorized" };

async function attempt<T>(
  call: UserApiCall<T>,
  accessToken: string,
): Promise<Attempt<T>> {
  let result: { data?: T; error?: unknown; response: Response };
  try {
    result = await call(userApiClient(accessToken));
  } catch (error) {
    console.warn(
      `API call as the user failed: ${error instanceof Error ? error.name : "unknown error"}`,
    );
    return { status: "failed" };
  }
  const { status } = result.response;
  if (status === 401) {
    return { status: "unauthorized" };
  }
  if (status === 503) {
    return { status: "unavailable" };
  }
  if (status === 404) {
    return { status: "not-found" };
  }
  if (status === 403) {
    return { status: "forbidden" };
  }
  if (status === 409) {
    return { status: "conflict", error: result.error };
  }
  if (REJECTED_STATUSES.has(status)) {
    return { status: "rejected", httpStatus: status, error: result.error };
  }
  if (status >= 200 && status < 300 && result.data !== undefined) {
    return { status: "ok", data: result.data };
  }
  console.warn(`API call as the user failed with status ${status}`);
  return { status: "failed" };
}

async function forgetSession(): Promise<void> {
  const domain = process.env.WORKOS_COOKIE_DOMAIN?.trim() ?? "";
  (await cookies()).delete({
    name: sessionCookieName(),
    path: "/",
    ...(domain === "" ? {} : { domain }),
  });
}

async function refreshedAccessToken(): Promise<
  | { status: "ok"; accessToken: string }
  | { status: "signed-out" }
  | { status: "unavailable" }
> {
  try {
    const { accessToken } = await refreshSession();
    return accessToken === undefined
      ? { status: "signed-out" }
      : { status: "ok", accessToken };
  } catch (error) {
    if (error instanceof TokenRefreshError && error.isTransient) {
      return { status: "unavailable" };
    }
    return { status: "signed-out" };
  }
}

export async function callApiAsUser<T>(
  call: UserApiCall<T>,
): Promise<UserApiResult<T>> {
  const { accessToken } = await withAuth();
  if (accessToken === undefined) {
    return { status: "signed-out" };
  }

  const first = await attempt(call, accessToken);
  if (first.status !== "unauthorized") {
    return first;
  }
  if ((await headers()).get(SESSION_STATE_HEADER) === "refreshed") {
    return { status: "signed-out" };
  }

  const refreshed = await refreshedAccessToken();
  if (refreshed.status === "unavailable") {
    return refreshed;
  }
  if (refreshed.status === "signed-out") {
    await forgetSession();
    return refreshed;
  }

  const second = await attempt(call, refreshed.accessToken);
  if (second.status === "unauthorized") {
    await forgetSession();
    return { status: "signed-out" };
  }
  return second;
}

export async function readApiAsUser<T>(
  call: UserApiCall<T>,
): Promise<UserApiResult<T>> {
  const { accessToken } = await withAuth();
  if (accessToken === undefined) {
    return (await headers()).get(SESSION_STATE_HEADER) === "unverified"
      ? { status: "unavailable" }
      : { status: "signed-out" };
  }
  const result = await attempt(call, accessToken);
  return result.status === "unauthorized" ? { status: "signed-out" } : result;
}
