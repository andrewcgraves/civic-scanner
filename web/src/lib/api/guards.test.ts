import { describe, expect, it } from "vitest";

import {
  isAgency,
  isAgencyList,
  isDateTime,
  isMeetingDetail,
  isMeetingList,
  isMeetingSummary,
} from "./guards";

const AGENCY = {
  id: "a",
  slug: "la-metro",
  name: "LA Metro",
  website: "https://www.metro.net",
};

const SUMMARY = {
  id: "m",
  title: "Board",
  startsAt: "2027-01-20T17:00:00Z",
  status: "scheduled",
};

const AGENDA = {
  version: 2,
  documentUrl: "https://example.org/a.pdf",
  summary: null,
  changeNote: "Item 3 added.",
  foundAt: "2027-01-13T18:30:00Z",
};

const DETAIL = {
  ...SUMMARY,
  sourceUrl: "https://example.org/m",
  agency: AGENCY,
  agenda: AGENDA,
  recordings: [
    { sourceUrl: "https://example.org/r1", transcript: null },
    {
      sourceUrl: "https://example.org/r2",
      transcript: {
        summary: "Said things.",
        segments: [{ startMs: null, endMs: 0, text: null }],
      },
    },
  ],
};

describe("isDateTime", () => {
  it("accepts parseable date strings only", () => {
    expect(isDateTime("2027-01-20T17:00:00Z")).toBe(true);
    expect(isDateTime("not a date")).toBe(false);
    expect(isDateTime(0)).toBe(false);
  });
});

describe("agency guards", () => {
  it("accept agencies and agency lists", () => {
    expect(isAgency(AGENCY)).toBe(true);
    expect(isAgencyList({ agencies: [AGENCY] })).toBe(true);
  });

  it.each([
    null,
    [],
    "x",
    { ...AGENCY, id: 1 },
    { ...AGENCY, slug: "" },
    { ...AGENCY, name: null },
    { ...AGENCY, website: undefined },
  ])("reject the agency %j", (value) => {
    expect(isAgency(value)).toBe(false);
  });

  it.each([{ agencies: null }, { agencies: [{}] }, [AGENCY], null])(
    "reject the agency list %j",
    (value) => {
      expect(isAgencyList(value)).toBe(false);
    },
  );
});

describe("meeting list guards", () => {
  it("accept meetings with or without a source URL, and null cursors", () => {
    expect(isMeetingSummary(SUMMARY)).toBe(true);
    expect(isMeetingSummary({ ...SUMMARY, sourceUrl: null })).toBe(true);
    expect(isMeetingSummary({ ...SUMMARY, status: "postponed" })).toBe(true);
    expect(isMeetingList({ meetings: [SUMMARY], nextCursor: null })).toBe(true);
    expect(isMeetingList({ meetings: [], nextCursor: "c" })).toBe(true);
  });

  it.each([
    { ...SUMMARY, id: "" },
    { ...SUMMARY, title: 3 },
    { ...SUMMARY, startsAt: "soon" },
    { ...SUMMARY, status: null },
    { ...SUMMARY, sourceUrl: 5 },
  ])("reject the meeting %j", (value) => {
    expect(isMeetingSummary(value)).toBe(false);
  });

  it.each([
    { meetings: null, nextCursor: null },
    { meetings: [{ id: "x" }], nextCursor: null },
    { meetings: [] },
  ])("reject the meeting list %j", (value) => {
    expect(isMeetingList(value)).toBe(false);
  });
});

describe("isMeetingDetail", () => {
  it("accepts a meeting with an agenda, recordings and odd segments", () => {
    expect(isMeetingDetail(DETAIL)).toBe(true);
    expect(isMeetingDetail({ ...DETAIL, agenda: null, recordings: [] })).toBe(
      true,
    );
  });

  it.each([
    ["no agency", { ...DETAIL, agency: null }],
    ["bad meeting fields", { ...DETAIL, startsAt: "not a date" }],
    ["recordings not a list", { ...DETAIL, recordings: null }],
    ["agenda version zero", { ...DETAIL, agenda: { ...AGENDA, version: 0 } }],
    [
      "agenda version fraction",
      { ...DETAIL, agenda: { ...AGENDA, version: 1.5 } },
    ],
    ["agenda version text", { ...DETAIL, agenda: { ...AGENDA, version: "1" } }],
    [
      "agenda document",
      { ...DETAIL, agenda: { ...AGENDA, documentUrl: null } },
    ],
    ["agenda summary", { ...DETAIL, agenda: { ...AGENDA, summary: 42 } }],
    ["agenda change note", { ...DETAIL, agenda: { ...AGENDA, changeNote: 1 } }],
    ["agenda date", { ...DETAIL, agenda: { ...AGENDA, foundAt: "x" } }],
    ["recording source", { ...DETAIL, recordings: [{ transcript: null }] }],
    [
      "transcript summary",
      {
        ...DETAIL,
        recordings: [
          { sourceUrl: "x", transcript: { summary: 1, segments: [] } },
        ],
      },
    ],
    [
      "transcript segments",
      {
        ...DETAIL,
        recordings: [
          { sourceUrl: "x", transcript: { summary: null, segments: [null] } },
        ],
      },
    ],
  ])("rejects %s", (_what, value) => {
    expect(isMeetingDetail(value)).toBe(false);
  });
});
