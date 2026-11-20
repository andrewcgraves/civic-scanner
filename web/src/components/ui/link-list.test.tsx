import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { LinkList, LinkListItem } from "./link-list";

describe("LinkList", () => {
  it("renders each item as one link containing its parts", () => {
    render(
      <LinkList label="Meetings">
        <LinkListItem
          href="/meetings/1"
          title="Board meeting"
          meta="Wed, Jan 20, 2027"
          aside={<span>Held</span>}
          description="Agenda posted"
        />
        <LinkListItem href="/meetings/2" title="Committee" />
      </LinkList>,
    );

    expect(screen.getByRole("list", { name: "Meetings" })).toBeVisible();
    expect(screen.getAllByRole("listitem")).toHaveLength(2);
    const link = screen.getByRole("link", { name: /Board meeting/ });
    expect(link).toHaveAttribute("href", "/meetings/1");
    expect(link).toHaveTextContent(
      "Board meetingHeldWed, Jan 20, 2027Agenda posted",
    );
    expect(screen.getByRole("link", { name: "Committee" })).toHaveAttribute(
      "href",
      "/meetings/2",
    );
  });

  it("renders an item without an href as a plain row", () => {
    render(
      <LinkList label="Meetings">
        <LinkListItem
          title="Board meeting"
          meta="Wed, Jan 20, 2027"
          aside={<span>Held</span>}
          description={<a href="https://example.com">Agency page</a>}
        />
      </LinkList>,
    );

    const item = screen.getByRole("listitem");
    expect(item).toHaveTextContent(
      "Board meetingHeldWed, Jan 20, 2027Agency page",
    );
    expect(screen.getAllByRole("link")).toHaveLength(1);
    expect(screen.getByRole("link", { name: "Agency page" })).toHaveAttribute(
      "href",
      "https://example.com",
    );
  });
});
