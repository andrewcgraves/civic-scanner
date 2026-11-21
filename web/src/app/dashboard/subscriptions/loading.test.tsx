import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import SubscriptionsLoading from "./loading";

describe("SubscriptionsLoading", () => {
  it("shows the subscriptions as loading", () => {
    render(<SubscriptionsLoading />);

    expect(
      screen.getByRole("heading", { level: 1, name: "Dashboard" }),
    ).toBeVisible();
    expect(
      screen.getByRole("heading", { level: 2, name: "Webhook subscriptions" }),
    ).toBeVisible();
    expect(screen.getByText("Loading your subscriptions…")).toBeVisible();
    expect(
      screen.getByText("Loading your subscriptions…").parentElement,
    ).toHaveAttribute("aria-busy", "true");
  });
});
