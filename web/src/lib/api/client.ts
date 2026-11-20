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
export type MeetingStatus = components["schemas"]["MeetingStatus"];
export type MeetingSummary = components["schemas"]["MeetingSummary"];
export type MeetingList = components["schemas"]["MeetingList"];
export type MeetingDetail = components["schemas"]["MeetingDetail"];
export type Agenda = components["schemas"]["Agenda"];
export type Recording = components["schemas"]["Recording"];
export type Transcript = components["schemas"]["Transcript"];
export type TranscriptSegment = components["schemas"]["TranscriptSegment"];
