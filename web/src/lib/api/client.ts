import createClient from "openapi-fetch";

import type { components, paths } from "./v1";

const DEFAULT_API_BASE_URL = "http://localhost:8080/v1";

const configuredBaseUrl = process.env.NEXT_PUBLIC_API_BASE_URL?.trim();

/** Base URL of the API, including the `/v1` prefix. Blank values fall back to the default. */
export const apiBaseUrl =
  configuredBaseUrl === undefined || configuredBaseUrl === ""
    ? DEFAULT_API_BASE_URL
    : configuredBaseUrl;

/** Typed client for the v1 API; paths and payloads come from `openapi/v1.yaml`. */
export const api = createClient<paths>({ baseUrl: apiBaseUrl });

export type Agency = components["schemas"]["Agency"];
export type ApiError = components["schemas"]["Error"];
