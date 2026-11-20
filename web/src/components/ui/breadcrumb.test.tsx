import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { Breadcrumb } from "./breadcrumb";

describe("Breadcrumb", () => {
  it("links each ancestor and marks the current page", () => {
    render(
      <Breadcrumb
        items={[
          { label: "Agencies", href: "/agencies" },
          { label: "LA Metro", href: "/agencies/la-metro" },
          { label: "Board meeting" },
        ]}
      />,
    );

    const nav = screen.getByRole("navigation", { name: "Breadcrumb" });
    expect(within(nav).getAllByRole("listitem")).toHaveLength(3);
    expect(within(nav).getByRole("link", { name: "LA Metro" })).toHaveAttribute(
      "href",
      "/agencies/la-metro",
    );
    expect(within(nav).getByText("Board meeting")).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(
      within(nav).queryByRole("link", { name: "Board meeting" }),
    ).toBeNull();
  });

  it("does not link the current page even when it has an href", () => {
    render(<Breadcrumb items={[{ label: "Agencies", href: "/agencies" }]} />);

    expect(screen.queryByRole("link")).toBeNull();
    expect(screen.getByText("Agencies")).toHaveAttribute(
      "aria-current",
      "page",
    );
  });

  it("shows an ancestor without an href as text", () => {
    render(<Breadcrumb items={[{ label: "Meetings" }, { label: "One" }]} />);

    expect(screen.queryByRole("link")).toBeNull();
    expect(screen.getByText("Meetings")).not.toHaveAttribute("aria-current");
  });
});
