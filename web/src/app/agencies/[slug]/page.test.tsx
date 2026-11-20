import { render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  flagsOn: new Set<string>(),
  notFound: vi.fn(() => {
    throw new Error("NEXT_NOT_FOUND");
  }),
}));

vi.mock("next/server", () => ({ connection: () => Promise.resolve() }));
vi.mock("next/navigation", () => ({ notFound: mocks.notFound }));
vi.mock("@/lib/flags/server", () => ({
  isFlagEnabled: (flag: string) => Promise.resolve(mocks.flagsOn.has(flag)),
  requireFlag: (flag: string) => {
    if (!mocks.flagsOn.has(flag)) {
      mocks.notFound();
    }
    return Promise.resolve();
  },
}));

const NOW = new Date("2027-01-15T18:00:00Z");
const API = "http://localhost:8080/v1/agencies/la-metro";

const AGENCY = {
  id: "6e37b2a6-08c1-4ac7-9374-a859aa9fb342",
  slug: "la-metro",
  name: "LA Metro",
  website: "https://www.metro.net",
};

function meeting(
  n: number,
  startsAt: string,
  status: "scheduled" | "held" | "cancelled",
) {
  return {
    id: `00000000-0000-4000-8000-${String(n).padStart(12, "0")}`,
    title: `Meeting ${String(n)}`,
    startsAt,
    status,
  };
}

const FUTURE_LATE = meeting(1, "2027-03-01T17:00:00Z", "scheduled");
const FUTURE_CANCELLED = meeting(2, "2027-02-01T17:00:00Z", "cancelled");
const FUTURE_SOON = meeting(3, "2027-01-20T17:00:00Z", "scheduled");
const HELD_ODD = meeting(4, "2027-01-16T17:00:00Z", "held");
const PAST_CANCELLED = meeting(5, "2027-01-10T17:00:00Z", "cancelled");
const PAST_HELD = meeting(6, "2026-12-10T17:00:00Z", "held");

type Responder = (url: string) => Response | Promise<Response>;

const json = (body: unknown, status = 200) => Response.json(body, { status });
const apiError = (status: number) => json({ code: "x", message: "x" }, status);

async function renderPage(
  respond: Responder,
  search: Record<string, string | string[]> = {},
  slug = "la-metro",
) {
  const fetchMock = vi.fn((request: Request) =>
    Promise.resolve(respond(request.url)),
  );
  vi.stubGlobal("fetch", fetchMock);
  vi.resetModules();
  const { default: AgencyPage } = await import("./page");
  render(
    await AgencyPage({
      params: Promise.resolve({ slug }),
      searchParams: Promise.resolve(search),
    }),
  );
  return fetchMock.mock.calls.map(([request]) => request.url);
}

function routes(meetingPages: Record<string, unknown>): Responder {
  return (url) => {
    if (url === API) {
      return json(AGENCY);
    }
    const page = meetingPages[url];
    return page === undefined ? apiError(500) : json(page);
  };
}

const sectionList = (name: string) => screen.getByRole("list", { name });

const rowTitles = (name: string) =>
  within(sectionList(name))
    .getAllByRole("listitem")
    .map((item) => /Meeting \d+/.exec(item.textContent)?.[0]);

describe("AgencyPage", () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(NOW);
    mocks.flagsOn = new Set(["agency-pages", "meeting-pages"]);
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    mocks.notFound.mockClear();
  });

  it("shows the agency with upcoming and past meetings linking to their pages", async () => {
    const requested = await renderPage(
      routes({
        [`${API}/meetings`]: {
          meetings: [
            FUTURE_LATE,
            FUTURE_CANCELLED,
            FUTURE_SOON,
            HELD_ODD,
            PAST_CANCELLED,
            PAST_HELD,
          ],
          nextCursor: null,
        },
      }),
    );

    expect(
      screen.getByRole("heading", { level: 1, name: "LA Metro" }),
    ).toBeVisible();
    const breadcrumb = screen.getByRole("navigation", { name: "Breadcrumb" });
    expect(
      within(breadcrumb).getByRole("link", { name: "Agencies" }),
    ).toHaveAttribute("href", "/agencies");
    expect(within(breadcrumb).getByText("LA Metro")).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(
      screen.getByRole("link", { name: /Agency website/ }),
    ).toHaveAttribute("href", "https://www.metro.net");
    expect(
      screen.queryByRole("link", { name: "RSS feed" }),
    ).not.toBeInTheDocument();

    expect(rowTitles("Upcoming meetings")).toEqual([
      "Meeting 3",
      "Meeting 2",
      "Meeting 1",
    ]);
    expect(rowTitles("Past meetings")).toEqual([
      "Meeting 4",
      "Meeting 5",
      "Meeting 6",
    ]);

    const soon = screen.getByRole("link", { name: /Meeting 3/ });
    expect(soon).toHaveAttribute(
      "href",
      "/meetings/00000000-0000-4000-8000-000000000003",
    );
    expect(soon).toHaveTextContent(
      "Meeting 3ScheduledWednesday, January 20, 2027 at 9:00 AM PST",
    );
    expect(screen.getByRole("link", { name: /Meeting 2/ })).toHaveTextContent(
      "Cancelled",
    );
    expect(screen.getByRole("link", { name: /Meeting 5/ })).toHaveTextContent(
      "Cancelled",
    );
    expect(screen.getByRole("link", { name: /Meeting 4/ })).toHaveTextContent(
      "Held",
    );
    expect(
      screen.queryByRole("navigation", { name: "Meeting pages" }),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(requested).toEqual([API, `${API}/meetings`]);
  });

  it("lists meetings without internal links when meeting pages are off", async () => {
    mocks.flagsOn = new Set(["agency-pages"]);
    await renderPage(
      routes({
        [`${API}/meetings`]: {
          meetings: [
            { ...FUTURE_SOON, sourceUrl: "https://metro.legistar.com/m/3" },
            PAST_HELD,
          ],
          nextCursor: null,
        },
      }),
    );

    expect(
      screen.queryByRole("link", { name: /Meeting \d/ }),
    ).not.toBeInTheDocument();
    expect(sectionList("Upcoming meetings")).toHaveTextContent(
      "Meeting 3ScheduledWednesday, January 20, 2027 at 9:00 AM PST",
    );
    expect(
      screen.getByRole("link", { name: /Meeting page on the agency's site/ }),
    ).toHaveAttribute("href", "https://metro.legistar.com/m/3");
    expect(sectionList("Past meetings")).toHaveTextContent("Meeting 6Held");
  });

  it("says when an agency has no meetings tracked yet", async () => {
    await renderPage(
      routes({ [`${API}/meetings`]: { meetings: [], nextCursor: null } }),
    );

    expect(
      screen.getByText("Meetings for this agency are not tracked yet."),
    ).toBeVisible();
    expect(
      screen.queryByRole("heading", { name: "Upcoming meetings" }),
    ).not.toBeInTheDocument();
  });

  it("says when there are no upcoming or no past meetings", async () => {
    await renderPage(
      routes({
        [`${API}/meetings`]: { meetings: [PAST_HELD], nextCursor: null },
      }),
    );
    expect(screen.getByText("No upcoming meetings are posted.")).toBeVisible();

    await renderPage(
      routes({
        [`${API}/meetings`]: { meetings: [FUTURE_SOON], nextCursor: null },
      }),
    );
    expect(
      screen.getByText("No past meetings are on record yet."),
    ).toBeVisible();
  });

  it("shows the meetings when the agency is unavailable", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    await renderPage((url) =>
      url === API
        ? apiError(503)
        : json({ meetings: [PAST_HELD], nextCursor: null }),
    );

    expect(
      screen.getByRole("heading", { level: 1, name: "Agency" }),
    ).toBeVisible();
    expect(screen.getByRole("alert")).toHaveTextContent(
      "This agency's details are unavailable right now.",
    );
    expect(
      screen.queryByRole("link", { name: /Agency website/ }),
    ).not.toBeInTheDocument();
    expect(rowTitles("Past meetings")).toEqual(["Meeting 6"]);
  });

  it("links the RSS feed when feeds are on", async () => {
    mocks.flagsOn = new Set(["agency-pages", "agency-rss-feeds"]);
    await renderPage(routes({}));

    expect(screen.getByRole("link", { name: "RSS feed" })).toHaveAttribute(
      "href",
      "/agencies/la-metro/feed.xml",
    );
  });

  it("links the RSS feed even when the agency is unavailable", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    mocks.flagsOn = new Set(["agency-pages", "agency-rss-feeds"]);
    await renderPage(() => apiError(503));

    expect(
      screen.queryByRole("link", { name: /Agency website/ }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "RSS feed" })).toBeVisible();
  });

  it("shows the agency when its meetings are unavailable", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    await renderPage((url) =>
      url === API ? json(AGENCY) : Promise.reject(new TypeError("down")),
    );

    expect(
      screen.getByRole("heading", { level: 1, name: "LA Metro" }),
    ).toBeVisible();
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Meetings are unavailable right now.",
    );
  });

  it("is not found for an unknown agency", async () => {
    await expect(renderPage(() => apiError(404))).rejects.toThrow(
      "NEXT_NOT_FOUND",
    );
  });

  it("is not found while the agency pages flag is off", async () => {
    mocks.flagsOn = new Set(["meeting-pages"]);
    await expect(renderPage(() => json(AGENCY))).rejects.toThrow(
      "NEXT_NOT_FOUND",
    );
    expect(fetch).not.toHaveBeenCalled();
  });

  it("links to older meetings from the first page", async () => {
    await renderPage(
      routes({
        [`${API}/meetings`]: {
          meetings: [FUTURE_SOON, PAST_HELD],
          nextCursor: "next_Page-1",
        },
      }),
    );

    const pages = screen.getByRole("navigation", { name: "Meeting pages" });
    expect(
      within(pages).getByRole("link", { name: "Older meetings" }),
    ).toHaveAttribute("href", "/agencies/la-metro?cursor=next_Page-1");
    expect(
      within(pages).queryByRole("link", { name: "Newest meetings" }),
    ).not.toBeInTheDocument();
  });

  it("loads further pages on the first page until it reaches a past meeting", async () => {
    const requested = await renderPage(
      routes({
        [`${API}/meetings`]: { meetings: [FUTURE_LATE], nextCursor: "c1" },
        [`${API}/meetings?cursor=c1`]: {
          meetings: [FUTURE_SOON, PAST_HELD],
          nextCursor: "c2",
        },
      }),
    );

    expect(requested).toEqual([
      API,
      `${API}/meetings`,
      `${API}/meetings?cursor=c1`,
    ]);
    expect(rowTitles("Upcoming meetings")).toEqual(["Meeting 3", "Meeting 1"]);
    expect(rowTitles("Past meetings")).toEqual(["Meeting 6"]);
    expect(
      screen.getByRole("link", { name: "Older meetings" }),
    ).toHaveAttribute("href", "/agencies/la-metro?cursor=c2");
  });

  it("shows an older page with a way back to the newest meetings", async () => {
    const requested = await renderPage(
      routes({
        [`${API}/meetings?cursor=c2`]: {
          meetings: [PAST_CANCELLED, PAST_HELD],
          nextCursor: "c3",
        },
      }),
      { cursor: "c2" },
    );

    expect(requested).toEqual([API, `${API}/meetings?cursor=c2`]);
    expect(
      screen.queryByRole("heading", { name: "Upcoming meetings" }),
    ).not.toBeInTheDocument();
    expect(rowTitles("Older meetings")).toEqual(["Meeting 5", "Meeting 6"]);
    const pages = screen.getByRole("navigation", { name: "Meeting pages" });
    expect(
      within(pages).getByRole("link", { name: "Newest meetings" }),
    ).toHaveAttribute("href", "/agencies/la-metro");
    expect(
      within(pages).getByRole("link", { name: "Older meetings" }),
    ).toHaveAttribute("href", "/agencies/la-metro?cursor=c3");
  });

  it("shows upcoming meetings that spill onto an older page", async () => {
    await renderPage(
      routes({
        [`${API}/meetings?cursor=c4`]: {
          meetings: [FUTURE_SOON, PAST_HELD],
          nextCursor: null,
        },
      }),
      { cursor: "c4" },
    );

    expect(rowTitles("Upcoming meetings")).toEqual(["Meeting 3"]);
    expect(rowTitles("Older meetings")).toEqual(["Meeting 6"]);
    expect(
      screen.queryByRole("link", { name: "Older meetings" }),
    ).not.toBeInTheDocument();
  });

  it("says when an older page is empty", async () => {
    await renderPage(
      routes({
        [`${API}/meetings?cursor=c5`]: { meetings: [], nextCursor: null },
      }),
      { cursor: "c5" },
    );

    expect(screen.getByText("There are no older meetings.")).toBeVisible();
    expect(
      screen.getByRole("link", { name: "Newest meetings" }),
    ).toHaveAttribute("href", "/agencies/la-metro");
  });

  it("is not found for a cursor the API rejects", async () => {
    await expect(
      renderPage((url) => (url === API ? json(AGENCY) : apiError(400)), {
        cursor: "tampered",
      }),
    ).rejects.toThrow("NEXT_NOT_FOUND");
  });

  it("is not found for a malformed cursor without asking the API", async () => {
    await expect(
      renderPage(() => json(AGENCY), { cursor: "not a cursor!" }),
    ).rejects.toThrow("NEXT_NOT_FOUND");
    await expect(
      renderPage(() => json(AGENCY), { cursor: ["a", "b"] }),
    ).rejects.toThrow("NEXT_NOT_FOUND");
    expect(fetch).not.toHaveBeenCalled();
  });
});

describe("generateMetadata", () => {
  beforeEach(() => {
    mocks.flagsOn = new Set(["agency-pages"]);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  async function metadataWith(respond: Responder) {
    vi.stubGlobal(
      "fetch",
      vi.fn((request: Request) => Promise.resolve(respond(request.url))),
    );
    vi.resetModules();
    const { generateMetadata } = await import("./page");
    return generateMetadata({ params: Promise.resolve({ slug: "la-metro" }) });
  }

  it("names the agency", async () => {
    expect(await metadataWith(() => json(AGENCY))).toEqual({
      title: "LA Metro",
      description:
        "Upcoming and past public meetings of LA Metro, tracked by Civic Scanner.",
    });
  });

  it("advertises the agency's RSS feed when feeds are on", async () => {
    mocks.flagsOn = new Set(["agency-pages", "agency-rss-feeds"]);
    expect(await metadataWith(() => json(AGENCY))).toMatchObject({
      title: "LA Metro",
      alternates: {
        types: {
          "application/rss+xml": [
            { url: "/agencies/la-metro/feed.xml", title: "LA Metro meetings" },
          ],
        },
      },
    });
  });

  it("advertises the feed with a generic title when the agency is unavailable", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    mocks.flagsOn = new Set(["agency-pages", "agency-rss-feeds"]);
    expect(await metadataWith(() => apiError(500))).toEqual({
      title: "Agency",
      alternates: {
        types: {
          "application/rss+xml": [
            { url: "/agencies/la-metro/feed.xml", title: "Agency meetings" },
          ],
        },
      },
    });
  });

  it("falls back to a generic title when the agency is unavailable", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    expect(await metadataWith(() => apiError(500))).toEqual({
      title: "Agency",
    });
  });

  it("is not found while the agency pages flag is off", async () => {
    mocks.flagsOn = new Set();
    await expect(metadataWith(() => json(AGENCY))).rejects.toThrow(
      "NEXT_NOT_FOUND",
    );
  });
});
