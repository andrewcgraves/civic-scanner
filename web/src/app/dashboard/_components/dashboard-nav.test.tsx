import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { DashboardNav } from "./dashboard-nav";

describe("DashboardNav", () => {
  it("marks the overview as the current page", () => {
    render(<DashboardNav billing />);

    const nav = screen.getByRole("navigation", { name: "Dashboard" });
    const overview = within(nav).getByRole("link", { name: "Overview" });
    expect(overview).toHaveAttribute("href", "/dashboard");
    expect(overview).toHaveAttribute("aria-current", "page");
  });

  it("shows subscriptions as coming soon, not as a link", () => {
    render(<DashboardNav billing />);

    const items = within(screen.getByRole("list")).getAllByRole("listitem");
    expect(items[1]).toHaveTextContent("Subscriptions Soon");
    expect(screen.queryByRole("link", { name: /Subscriptions/ })).toBeNull();
  });

  it("links to subscriptions when they are on", () => {
    render(<DashboardNav billing subscriptions />);

    const link = screen.getByRole("link", { name: "Subscriptions" });
    expect(link).toHaveAttribute("href", "/dashboard/subscriptions");
    expect(link).not.toHaveAttribute("aria-current");
    expect(screen.queryByText("Soon")).toBeNull();
  });

  it("marks subscriptions as the current page", () => {
    render(<DashboardNav billing subscriptions current="subscriptions" />);

    expect(screen.getByRole("link", { name: "Subscriptions" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(screen.getByRole("link", { name: "Overview" })).not.toHaveAttribute(
      "aria-current",
    );
  });

  it("links to billing when billing is on", () => {
    render(<DashboardNav billing />);

    const billing = screen.getByRole("link", { name: "Billing" });
    expect(billing).toHaveAttribute("href", "/billing");
    expect(billing).not.toHaveAttribute("aria-current");
  });

  it("has no billing tab when billing is off", () => {
    render(<DashboardNav billing={false} />);

    expect(screen.queryByRole("link", { name: "Billing" })).toBeNull();
    expect(screen.getAllByRole("listitem")).toHaveLength(2);
  });
});
