import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  startCheckoutAction: vi.fn(),
  openBillingPortalAction: vi.fn(),
}));

vi.mock("@/lib/billing/actions", () => ({
  startCheckoutAction: mocks.startCheckoutAction,
  openBillingPortalAction: mocks.openBillingPortalAction,
}));

import { PlanCard, PlanCardLoading } from "./plan-card";

describe("PlanCard", () => {
  it("offers the paid plan to a free user", () => {
    render(<PlanCard plan="free" billing />);

    expect(screen.getByText("Free plan")).toBeVisible();
    expect(screen.getByText("Current")).toBeVisible();
    expect(screen.getByText(/free to read/)).toBeVisible();
    const start = screen.getByRole("button", { name: "Start the paid plan" });
    expect(start).toHaveAttribute("type", "submit");
    expect(start.closest("form")).not.toBeNull();
    expect(
      screen.getByText("Payment is taken on a secure Stripe page."),
    ).toBeVisible();
    expect(
      screen.queryByRole("button", { name: "Open the billing portal" }),
    ).toBeNull();
  });

  it("offers the billing portal to a paid user", () => {
    render(<PlanCard plan="paid" billing />);

    expect(screen.getByText("Paid plan")).toBeVisible();
    expect(screen.getByText(/Thank you for supporting/)).toBeVisible();
    const portal = screen.getByRole("button", {
      name: "Open the billing portal",
    });
    expect(portal.closest("form")).not.toBeNull();
    expect(screen.getByText(/Stripe's billing portal/)).toBeVisible();
    expect(
      screen.queryByRole("button", { name: "Start the paid plan" }),
    ).toBeNull();
  });

  it.each([
    ["free" as const, /coming soon\.$/, /adds webhook subscriptions\.$/],
    ["paid" as const, /when they arrive\.$/, /included in your plan\.$/],
  ])(
    "says subscriptions are live for the %s plan once they are on",
    (plan, soon, live) => {
      const { unmount } = render(<PlanCard plan={plan} billing />);
      expect(screen.getByText(soon)).toBeVisible();
      unmount();

      render(<PlanCard plan={plan} billing subscriptions />);
      expect(screen.getByText(live)).toBeVisible();
      expect(screen.queryByText(soon)).toBeNull();
    },
  );

  it.each([["free" as const], ["paid" as const]])(
    "shows the %s plan without a button when billing is off",
    (plan) => {
      render(<PlanCard plan={plan} billing={false} />);

      expect(screen.getByText("Current")).toBeVisible();
      expect(screen.queryByRole("button")).toBeNull();
    },
  );
});

describe("PlanCardLoading", () => {
  it("says the plan is loading", () => {
    const { container } = render(<PlanCardLoading />);

    expect(screen.getByText("Loading your plan…")).toBeVisible();
    expect(container.firstChild).toHaveAttribute("aria-busy", "true");
    expect(screen.queryByRole("button")).toBeNull();
  });
});
