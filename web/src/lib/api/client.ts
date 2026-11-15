import createClient from "openapi-fetch";

import type { components, paths } from "./v1";

const DEFAULT_API_BASE_URL = "http://localhost:8080/v1";

const configuredBaseUrl = process.env.NEXT_PUBLIC_API_BASE_URL?.trim();

export const apiBaseUrl =
  configuredBaseUrl === undefined || configuredBaseUrl === ""
    ? DEFAULT_API_BASE_URL
    : configuredBaseUrl;

export const api = createClient<paths>({ baseUrl: apiBaseUrl });

export type Agency = components["schemas"]["Agency"];
export type ApiError = components["schemas"]["Error"];
