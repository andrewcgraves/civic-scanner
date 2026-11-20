import { afterEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  loadAgencyMeetings: vi.fn(),
  notFound: vi.fn(() => {
    throw new Error("NEXT_NOT_FOUND");
  }),
}));

vi.mock("next/navigation", () => ({ notFound: mocks.notFound }));
vi.mock("@/lib/api/load", () => ({
  loadAgencyMeetings: mocks.loadAgencyMeetings,
}));

import {
  MAX_FIRST_PAGE_REQUESTS,
  agencyPageHref,
  loadMeetingPage,
  parseCursor,
} from "./meeting-pages";

const NOW = new Date("2027-01-15T18:00:00Z");

function upcoming(n: number) {
  return {
    id: `up-${String(n)}`,
    title: "Board",
    startsAt: "2027-02-01T17:00:00Z",
    status: "scheduled" as const,
  };
}

const PAST = {
  id: "past",
  title: "Board",
  startsAt: "2026-12-01T17:00:00Z",
  status: "held" as const,
};

const page = (meetings: unknown[], nextCursor: string | null) => ({
  ok: true,
  data: { meetings, nextCursor },
});

afterEach(() => {
  mocks.loadAgencyMeetings.mockReset();
});

describe("parseCursor", () => {
  it("accepts no cursor and URL-safe base64 cursors", () => {
    expect(parseCursor(undefined)).toBeUndefined();
    expect(parseCursor("eyJhIjoiYiJ9_-x")).toBe("eyJhIjoiYiJ9_-x");
  });

  it("is not found for anything else", () => {
    for (const value of ["", "a b", "a+b/c=", "x".repeat(513), ["a", "b"]]) {
      expect(() => parseCursor(value)).toThrow("NEXT_NOT_FOUND");
    }
  });
});

describe("agencyPageHref", () => {
  it("links to the agency page with an optional cursor", () => {
    expect(agencyPageHref("la-metro")).toBe("/agencies/la-metro");
    expect(agencyPageHref("la-metro", "abc_-1")).toBe(
      "/agencies/la-metro?cursor=abc_-1",
    );
  });
});

describe("loadMeetingPage", () => {
  it("loads only the requested page for a cursor", async () => {
    mocks.loadAgencyMeetings.mockResolvedValue(page([upcoming(1)], "c2"));

    expect(await loadMeetingPage("la-metro", "c1", NOW)).toEqual(
      page([upcoming(1)], "c2"),
    );
    expect(mocks.loadAgencyMeetings).toHaveBeenCalledExactlyOnceWith(
      "la-metro",
      "c1",
    );
  });

  it("passes on an unavailable first page", async () => {
    mocks.loadAgencyMeetings.mockResolvedValue({ ok: false });

    expect(await loadMeetingPage("la-metro", undefined, NOW)).toEqual({
      ok: false,
    });
  });

  it("stops after the first page when it reaches a past meeting", async () => {
    mocks.loadAgencyMeetings.mockResolvedValue(page([upcoming(1), PAST], "c"));

    expect(await loadMeetingPage("la-metro", undefined, NOW)).toEqual(
      page([upcoming(1), PAST], "c"),
    );
    expect(mocks.loadAgencyMeetings).toHaveBeenCalledOnce();
  });

  it("follows pages of upcoming meetings up to the cap", async () => {
    for (let n = 1; n <= MAX_FIRST_PAGE_REQUESTS; n++) {
      mocks.loadAgencyMeetings.mockResolvedValueOnce(
        page([upcoming(n)], `c${String(n)}`),
      );
    }

    const result = await loadMeetingPage("la-metro", undefined, NOW);

    expect(result).toEqual(
      page([1, 2, 3, 4].map(upcoming), `c${String(MAX_FIRST_PAGE_REQUESTS)}`),
    );
    expect(mocks.loadAgencyMeetings.mock.calls).toEqual([
      ["la-metro", undefined],
      ["la-metro", "c1"],
      ["la-metro", "c2"],
      ["la-metro", "c3"],
    ]);
  });

  it("stops at the last page", async () => {
    mocks.loadAgencyMeetings
      .mockResolvedValueOnce(page([upcoming(1)], "c1"))
      .mockResolvedValueOnce(page([upcoming(2)], null));

    expect(await loadMeetingPage("la-metro", undefined, NOW)).toEqual(
      page([upcoming(1), upcoming(2)], null),
    );
  });

  it("is unavailable when a further page is", async () => {
    mocks.loadAgencyMeetings
      .mockResolvedValueOnce(page([upcoming(1)], "c1"))
      .mockResolvedValueOnce({ ok: false });

    expect(await loadMeetingPage("la-metro", undefined, NOW)).toEqual({
      ok: false,
    });
  });

  it("stops on an empty first page", async () => {
    mocks.loadAgencyMeetings.mockResolvedValue(page([], "c1"));

    expect(await loadMeetingPage("la-metro", undefined, NOW)).toEqual(
      page([], "c1"),
    );
    expect(mocks.loadAgencyMeetings).toHaveBeenCalledOnce();
  });
});
