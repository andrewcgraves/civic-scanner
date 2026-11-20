import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { AgencyMeetingList } from "./agency-meeting-list";

const MEETINGS = [
  {
    id: "11111111-1111-4111-8111-111111111111",
    title: "Board of Directors",
    startsAt: "2027-01-20T17:00:00Z",
    status: "cancelled" as const,
    sourceUrl: "https://metro.legistar.com/m/1",
  },
  {
    id: "22222222-2222-4222-8222-222222222222",
    title: "Finance Committee",
    startsAt: "2027-01-08T17:00:00Z",
    status: "held" as const,
  },
];

describe("AgencyMeetingList", () => {
  it("links each meeting to its page", () => {
    render(
      <AgencyMeetingList
        label="Upcoming meetings"
        meetings={MEETINGS}
        linkToMeetings
      />,
    );

    const list = screen.getByRole("list", { name: "Upcoming meetings" });
    const links = within(list).getAllByRole("link");
    expect(links.map((link) => link.getAttribute("href"))).toEqual([
      "/meetings/11111111-1111-4111-8111-111111111111",
      "/meetings/22222222-2222-4222-8222-222222222222",
    ]);
    expect(links[0]).toHaveTextContent(
      "Board of DirectorsCancelledWednesday, January 20, 2027 at 9:00 AM PST",
    );
    expect(links[0]?.querySelector("time")).toHaveAttribute(
      "dateTime",
      "2027-01-20T17:00:00Z",
    );
  });

  it("lists meetings without links to their pages", () => {
    render(
      <AgencyMeetingList
        label="Past meetings"
        meetings={MEETINGS}
        linkToMeetings={false}
      />,
    );

    const items = within(
      screen.getByRole("list", { name: "Past meetings" }),
    ).getAllByRole("listitem");
    expect(items[0]).toHaveTextContent(
      "Board of DirectorsCancelledWednesday, January 20, 2027 at 9:00 AM PST",
    );
    expect(within(items[0]!).getByRole("link")).toHaveAttribute(
      "href",
      "https://metro.legistar.com/m/1",
    );
    expect(items[1]).toHaveTextContent("Finance CommitteeHeld");
    expect(within(items[1]!).queryByRole("link")).not.toBeInTheDocument();
  });

  it("encodes meeting ids in links", () => {
    render(
      <AgencyMeetingList
        label="Past meetings"
        meetings={[{ ...MEETINGS[1]!, id: "../../evil?x=1#y" }]}
        linkToMeetings
      />,
    );

    expect(screen.getByRole("link")).toHaveAttribute(
      "href",
      "/meetings/..%2F..%2Fevil%3Fx%3D1%23y",
    );
  });

  it("leaves out a meeting's own page when its address is not a web link", () => {
    render(
      <AgencyMeetingList
        label="Past meetings"
        meetings={[{ ...MEETINGS[0]!, sourceUrl: "javascript:alert(1)" }]}
        linkToMeetings={false}
      />,
    );

    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(
      screen.queryByText(/Meeting page on the agency/),
    ).not.toBeInTheDocument();
  });
});
