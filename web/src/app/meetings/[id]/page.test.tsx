import { render, screen, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { MeetingDetail } from "@/lib/api/client";

const next = vi.hoisted(() => ({
  flagsOn: new Set<string>(),
  notFound: vi.fn(() => {
    throw new Error("NEXT_NOT_FOUND");
  }),
  transcriptSection: vi.fn<(props: { meeting: MeetingDetail }) => ReactNode>(
    () => <p>transcript section</p>,
  ),
}));

vi.mock("next/server", () => ({ connection: () => Promise.resolve() }));
vi.mock("next/navigation", () => ({ notFound: next.notFound }));
vi.mock("@/lib/flags/server", () => ({
  isFlagEnabled: (flag: string) => Promise.resolve(next.flagsOn.has(flag)),
  requireFlag: (flag: string) => {
    if (!next.flagsOn.has(flag)) {
      next.notFound();
    }
    return Promise.resolve();
  },
}));
vi.mock("./_components/transcript-section", () => ({
  TranscriptSection: next.transcriptSection,
}));

const MEETING_ID = "ad3d45f8-3ca9-4831-af75-0cd359f2f44a";

const MEETING: MeetingDetail = {
  id: MEETING_ID,
  agency: {
    id: "6e37b2a6-08c1-4ac7-9374-a859aa9fb342",
    slug: "la-metro",
    name: "LA Metro",
    website: "https://www.metro.net",
  },
  title: "Regular Board Meeting",
  startsAt: "2027-01-20T17:00:00Z",
  status: "scheduled",
  sourceUrl: "https://metro.legistar.com/MeetingDetail.aspx?ID=1",
  agenda: {
    version: 1,
    documentUrl: "https://metro.legistar.com/agenda.pdf",
    summary: "The board will vote on the budget.",
    changeNote: null,
    foundAt: "2027-01-13T18:30:00Z",
  },
  recordings: [],
};

const ALL_FLAGS = "meeting-pages,agency-pages";

function respondWith(body: unknown, status = 200) {
  return () => Promise.resolve(Response.json(body, { status }));
}

async function loadPage(flags: string, fetchImpl: typeof fetch) {
  next.flagsOn = new Set(flags.split(","));
  const fetchMock = vi.fn(fetchImpl);
  vi.stubGlobal("fetch", fetchMock);
  vi.resetModules();
  const page = await import("./page");
  const params = Promise.resolve({ id: MEETING_ID });
  return { ...page, params, fetchMock };
}

async function renderPage(
  fetchImpl: typeof fetch = respondWith(MEETING),
  flags = ALL_FLAGS,
) {
  const {
    default: MeetingPage,
    params,
    fetchMock,
  } = await loadPage(flags, fetchImpl);
  render(await MeetingPage({ params }));
  return fetchMock;
}

describe("MeetingPage", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    next.transcriptSection.mockClear();
  });

  it("shows the meeting header from the API", async () => {
    const fetchMock = await renderPage();

    expect(
      screen.getByRole("heading", { level: 1, name: "Regular Board Meeting" }),
    ).toBeVisible();
    expect(screen.getByText("Scheduled")).toBeInTheDocument();
    expect(
      screen.getByText("Wednesday, January 20, 2027 at 9:00 AM PST"),
    ).toHaveAttribute("dateTime", "2027-01-20T17:00:00Z");
    expect(
      screen.getByRole("link", { name: /This meeting on LA Metro's site/ }),
    ).toHaveAttribute("href", MEETING.sourceUrl);
    expect(screen.queryByText(/has been cancelled/)).not.toBeInTheDocument();
    const [request] = fetchMock.mock.calls[0] ?? [];
    expect((request as Request).url).toBe(
      `http://localhost:8080/v1/meetings/${MEETING_ID}`,
    );
  });

  it("links the agency in the breadcrumb when agency pages are on", async () => {
    await renderPage();

    const trail = screen.getByRole("navigation", { name: "Breadcrumb" });
    expect(
      within(trail).getByRole("link", { name: "Agencies" }),
    ).toHaveAttribute("href", "/agencies");
    expect(
      within(trail).getByRole("link", { name: "LA Metro" }),
    ).toHaveAttribute("href", "/agencies/la-metro");
    expect(within(trail).getByText("Regular Board Meeting")).toHaveAttribute(
      "aria-current",
      "page",
    );
  });

  it("names the agency without a link when agency pages are off", async () => {
    await renderPage(respondWith(MEETING), "meeting-pages");

    const trail = screen.getByRole("navigation", { name: "Breadcrumb" });
    expect(
      within(trail).queryByRole("link", { name: "LA Metro" }),
    ).not.toBeInTheDocument();
    expect(within(trail).getByText("LA Metro")).toBeInTheDocument();
    expect(within(trail).getByRole("link", { name: "Agencies" })).toBeVisible();
  });

  it("leaves out the agency's meeting link when there is no source URL", async () => {
    await renderPage(respondWith({ ...MEETING, sourceUrl: undefined }));

    expect(screen.queryByText(/This meeting on/)).not.toBeInTheDocument();
  });

  it("says prominently that a cancelled meeting is cancelled", async () => {
    await renderPage(respondWith({ ...MEETING, status: "cancelled" }));

    expect(screen.getByText("Cancelled")).toBeInTheDocument();
    expect(
      screen.getByText("This meeting has been cancelled"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/LA Metro lists this meeting as cancelled/),
    ).toBeInTheDocument();
  });

  it("renders the agenda section and then the transcript section with the meeting", async () => {
    await renderPage();

    const agenda = screen.getByRole("region", { name: "Agenda" });
    expect(
      within(agenda).getByText("The board will vote on the budget."),
    ).toBeInTheDocument();
    expect(next.transcriptSection).toHaveBeenCalledTimes(1);
    expect(next.transcriptSection.mock.calls[0]?.[0]).toEqual({
      meeting: MEETING,
    });
    const transcript = screen.getByText("transcript section");
    expect(
      agenda.compareDocumentPosition(transcript) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("shows the no-agenda state when no agenda is published", async () => {
    await renderPage(respondWith({ ...MEETING, agenda: null }));

    expect(screen.getByText("No agenda yet")).toBeInTheDocument();
  });

  it("shows an alert when the API is unavailable", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    await renderPage(
      respondWith({ code: "internal_error", message: "boom" }, 500),
    );

    expect(screen.getByRole("alert")).toHaveTextContent(
      "This meeting is unavailable right now. Please try again later.",
    );
    expect(
      screen.getByRole("heading", { level: 1, name: "Meeting" }),
    ).toBeVisible();
    expect(next.transcriptSection).not.toHaveBeenCalled();
  });

  it("is not found when the API has no such meeting", async () => {
    const { default: MeetingPage, params } = await loadPage(
      ALL_FLAGS,
      respondWith({ code: "not_found", message: "not found" }, 404),
    );

    await expect(MeetingPage({ params })).rejects.toThrow("NEXT_NOT_FOUND");
  });

  it("is not found for a malformed id without a request", async () => {
    const { default: MeetingPage, fetchMock } = await loadPage(
      ALL_FLAGS,
      respondWith(MEETING),
    );

    await expect(
      MeetingPage({ params: Promise.resolve({ id: "not-a-uuid" }) }),
    ).rejects.toThrow("NEXT_NOT_FOUND");
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("is not found when meeting pages are off", async () => {
    const {
      default: MeetingPage,
      params,
      fetchMock,
    } = await loadPage("agency-pages", respondWith(MEETING));

    await expect(MeetingPage({ params })).rejects.toThrow("NEXT_NOT_FOUND");
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("generateMetadata", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("names the meeting, agency and date", async () => {
    const { generateMetadata, params } = await loadPage(
      ALL_FLAGS,
      respondWith(MEETING),
    );

    expect(await generateMetadata({ params })).toEqual({
      title: "Regular Board Meeting — LA Metro",
      description:
        "Regular Board Meeting, a meeting of LA Metro on Wednesday, January 20, 2027.",
    });
  });

  it("describes a cancelled meeting as cancelled", async () => {
    const { generateMetadata, params } = await loadPage(
      ALL_FLAGS,
      respondWith({ ...MEETING, status: "cancelled" }),
    );

    expect(await generateMetadata({ params })).toMatchObject({
      description:
        "Regular Board Meeting, a meeting of LA Metro scheduled for Wednesday, January 20, 2027, has been cancelled.",
    });
  });

  it("falls back to a plain title when the API is unavailable", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const { generateMetadata, params } = await loadPage(ALL_FLAGS, () =>
      Promise.reject(new TypeError("fetch failed")),
    );

    expect(await generateMetadata({ params })).toEqual({
      title: "Meeting",
    });
  });

  it("is not found when meeting pages are off", async () => {
    const { generateMetadata, params, fetchMock } = await loadPage(
      "",
      respondWith(MEETING),
    );

    await expect(generateMetadata({ params })).rejects.toThrow(
      "NEXT_NOT_FOUND",
    );
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
