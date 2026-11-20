import type {
  Agency,
  Agenda,
  MeetingDetail,
  MeetingList,
  MeetingSummary,
  Recording,
  Transcript,
} from "./client";

type Fields = Record<string, unknown>;

function isObject(value: unknown): value is Fields {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isString(value: unknown): value is string {
  return typeof value === "string";
}

function isOptionalString(value: unknown): boolean {
  return value === undefined || value === null || isString(value);
}

function isNullableString(value: unknown): boolean {
  return value === null || isString(value);
}

export function isDateTime(value: unknown): value is string {
  return isString(value) && !Number.isNaN(Date.parse(value));
}

function isArrayOf<T>(
  value: unknown,
  check: (item: unknown) => item is T,
): value is T[] {
  return Array.isArray(value) && value.every(check);
}

export function isAgency(value: unknown): value is Agency {
  return (
    isObject(value) &&
    isString(value.id) &&
    isString(value.slug) &&
    value.slug !== "" &&
    isString(value.name) &&
    isString(value.website)
  );
}

export function isAgencyList(value: unknown): value is { agencies: Agency[] } {
  return isObject(value) && isArrayOf(value.agencies, isAgency);
}

function hasMeetingFields(value: Fields): boolean {
  return (
    isString(value.id) &&
    value.id !== "" &&
    isString(value.title) &&
    isDateTime(value.startsAt) &&
    isString(value.status) &&
    isOptionalString(value.sourceUrl)
  );
}

export function isMeetingSummary(value: unknown): value is MeetingSummary {
  return isObject(value) && hasMeetingFields(value);
}

export function isMeetingList(value: unknown): value is MeetingList {
  return (
    isObject(value) &&
    isArrayOf(value.meetings, isMeetingSummary) &&
    isNullableString(value.nextCursor)
  );
}

function isAgenda(value: unknown): value is Agenda {
  return (
    isObject(value) &&
    typeof value.version === "number" &&
    Number.isInteger(value.version) &&
    value.version >= 1 &&
    isString(value.documentUrl) &&
    isNullableString(value.summary) &&
    isNullableString(value.changeNote) &&
    isDateTime(value.foundAt)
  );
}

function isTranscript(value: unknown): value is Transcript {
  return (
    isObject(value) &&
    isNullableString(value.summary) &&
    isArrayOf(value.segments, isObject)
  );
}

function isRecording(value: unknown): value is Recording {
  return (
    isObject(value) &&
    isString(value.sourceUrl) &&
    (value.transcript === null || isTranscript(value.transcript))
  );
}

export function isMeetingDetail(value: unknown): value is MeetingDetail {
  return (
    isObject(value) &&
    hasMeetingFields(value) &&
    isAgency(value.agency) &&
    (value.agenda === null || isAgenda(value.agenda)) &&
    isArrayOf(value.recordings, isRecording)
  );
}
