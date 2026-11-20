import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { AgencyMeetings } from "./agency-meetings";

const NOW = new Date("2027-01-15T18:00:00Z");

const UPCOMING = {
  id: "11111111-1111-4111-8111-111111111111",
  title: "Board of Directors",
  startsAt: "2027-01-20T17:00:00Z",
  status: "scheduled" as const,
};

const PAST = {
  id: "22222222-2222-4222-8222-222222222222",
  title: "Finance Committee",
  startsAt: "2027-01-08T17:00:00Z",
  status: "held" as const,
};

function renderMeetings(
  meetings: (typeof UPCOMING | typeof PAST)[] | null,
  {
    laterPage = false,
    nextCursor = null as string | null,
    incomplete = false,
    moreUpcoming = false,
  } = {},
) {
  render(
    <AgencyMeetings
      slug="la-metro"
      page={
        meetings === null
          ? { ok: false }
          : {
              ok: true,
              data: { meetings, nextCursor, incomplete, moreUpcoming },
            }
      }
      laterPage={laterPage}
      now={NOW}
      linkToMeetings
    />,
  );
}

describe("AgencyMeetings", () => {
  it("splits the first page into upcoming and past meetings", () => {
    renderMeetings([UPCOMING, PAST]);

    expect(
      within(screen.getByRole("list", { name: "Upcoming meetings" })).getByRole(
        "link",
      ),
    ).toHaveTextContent("Board of Directors");
    expect(
      within(screen.getByRole("list", { name: "Past meetings" })).getByRole(
        "link",
      ),
    ).toHaveTextContent("Finance Committee");
    expect(
      screen.queryByRole("navigation", { name: "Meeting pages" }),
    ).not.toBeInTheDocument();
  });

  it("says when no meetings are tracked yet", () => {
    renderMeetings([]);

    expect(
      screen.getByRole("heading", { level: 2, name: "Meetings" }),
    ).toBeVisible();
    expect(
      screen.getByText("Meetings for this agency are not tracked yet."),
    ).toBeVisible();
  });

  it("says when the meetings are unavailable", () => {
    renderMeetings(null);

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Meetings are unavailable right now. Please try again later.",
    );
  });

  it("keeps the meetings that loaded and says the list may be incomplete", () => {
    renderMeetings([UPCOMING], { nextCursor: "c1", incomplete: true });

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Some meetings could not be loaded, so this list may be incomplete.",
    );
    expect(
      within(screen.getByRole("list", { name: "Upcoming meetings" })).getByRole(
        "link",
      ),
    ).toHaveTextContent("Board of Directors");
  });

  it("does not claim the soonest meetings or an empty past when more pages follow", () => {
    renderMeetings([UPCOMING], { nextCursor: "c4", moreUpcoming: true });

    expect(
      screen.getByText(
        "Soonest first among those shown. More upcoming meetings, including sooner ones, continue on the next page.",
      ),
    ).toBeVisible();
    expect(screen.queryByText("Soonest first.")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("heading", { name: "Past meetings" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByText("No past meetings are on record yet."),
    ).not.toBeInTheDocument();
    expect(
      within(
        screen.getByRole("navigation", { name: "Meeting pages" }),
      ).getByRole("link", { name: "Older meetings" }),
    ).toHaveAttribute("href", "/agencies/la-metro?cursor=c4");
  });

  it("links between pages of older meetings", () => {
    renderMeetings([PAST], { laterPage: true, nextCursor: "c2" });

    expect(
      screen.queryByRole("heading", { name: "Upcoming meetings" }),
    ).not.toBeInTheDocument();
    const pages = screen.getByRole("navigation", { name: "Meeting pages" });
    expect(
      within(pages).getByRole("link", { name: "Newest meetings" }),
    ).toHaveAttribute("href", "/agencies/la-metro");
    expect(
      within(pages).getByRole("link", { name: "Older meetings" }),
    ).toHaveAttribute("href", "/agencies/la-metro?cursor=c2");
  });
});
