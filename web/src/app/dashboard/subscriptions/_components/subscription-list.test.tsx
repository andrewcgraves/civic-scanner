import { render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import type { Subscription } from "../state";

vi.mock("./subscription-actions", () => ({
  SubscriptionActions: ({
    agencyName,
    paused,
  }: {
    agencyName: string;
    paused: boolean;
  }) => <p>{`actions for ${agencyName}${paused ? ", paused" : ""}`}</p>,
}));

const { AddButton, SubscriptionList } = await import("./subscription-list");

function subscription(
  index: number,
  overrides: Partial<Subscription> = {},
): Subscription {
  return {
    id: `00000000-0000-4000-8000-${String(index).padStart(12, "0")}`,
    agency: "la-metro",
    url: `https://hooks.example.org/${String(index)}`,
    payloadVersion: "v1",
    format: "standard",
    status: "active",
    pausedReason: null,
    pausedAt: null,
    consecutiveFailedEvents: 0,
    createdAt: "2026-11-03T18:00:00Z",
    updatedAt: "2026-11-03T18:00:00Z",
    secretRotatedAt: "2026-11-14T18:00:00Z",
    ...overrides,
  };
}

const NAMES = new Map([["la-metro", "LA Metro"]]);

describe("SubscriptionList", () => {
  it("lists each subscription with its agency, URL, status and dates", () => {
    render(
      <SubscriptionList
        subscriptions={[
          subscription(1),
          subscription(2, {
            agency: "metrolink",
            status: "paused",
            format: "discord",
          }),
        ]}
        names={NAMES}
      />,
    );

    const items = screen.getAllByRole("listitem");
    expect(items).toHaveLength(2);
    const [first, second] = items as [HTMLElement, HTMLElement];
    expect(
      within(first).getByRole("heading", { level: 3, name: "LA Metro" }),
    ).toBeVisible();
    expect(first).toHaveTextContent("Active");
    expect(first).toHaveTextContent("https://hooks.example.org/1");
    expect(first).toHaveTextContent(
      /Standard JSON · added .*Nov 3, 2026 · secret made .*Nov 14, 2026/,
    );
    expect(first).toHaveTextContent("actions for LA Metro");
    expect(
      within(second).getByRole("heading", { level: 3, name: "metrolink" }),
    ).toBeVisible();
    expect(second).toHaveTextContent("Paused");
    expect(second).toHaveTextContent(/Discord · added/);
    expect(second).toHaveTextContent("actions for metrolink, paused");
    expect(screen.getByText("You are using 2 of 25.")).toBeVisible();
  });

  it("says when the limit is reached", () => {
    render(
      <SubscriptionList
        subscriptions={Array.from({ length: 25 }, (_, index) =>
          subscription(index),
        )}
        names={NAMES}
      />,
    );

    expect(
      screen.getByText("You are using all 25. Delete one to add another."),
    ).toBeVisible();
  });

  it("invites a first subscription when there are none", () => {
    render(<SubscriptionList subscriptions={[]} names={NAMES} />);

    expect(screen.getByText("You have no subscriptions yet")).toBeVisible();
    expect(
      screen.getByRole("link", { name: "Add a subscription" }),
    ).toHaveAttribute("href", "/dashboard/subscriptions/new");
    expect(screen.queryByRole("list")).toBeNull();
  });
});

describe("AddButton", () => {
  it("links to the new subscription page below the limit", () => {
    render(<AddButton count={24} />);

    expect(
      screen.getByRole("link", { name: "Add a subscription" }),
    ).toHaveAttribute("href", "/dashboard/subscriptions/new");
  });

  it("is disabled at the limit", () => {
    render(<AddButton count={25} />);

    const button = screen.getByRole("button", { name: "Add a subscription" });
    expect(button).toBeDisabled();
    expect(button).toHaveAttribute("aria-describedby", "subscription-limit");
    expect(screen.queryByRole("link")).toBeNull();
  });
});
