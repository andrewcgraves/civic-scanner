import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  signIn: true,
  billing: true,
  subscriptions: false,
  cookies: new Set<string>(["wos-session"]),
  redirect: vi.fn((url: string) => {
    throw new Error(`redirect ${url}`);
  }),
  notFound: vi.fn(() => {
    throw new Error("not found");
  }),
}));

vi.mock("next/navigation", () => ({
  redirect: mocks.redirect,
  notFound: mocks.notFound,
}));
vi.mock("next/headers", () => ({
  cookies: () =>
    Promise.resolve({ has: (name: string) => mocks.cookies.has(name) }),
}));
vi.mock("@/lib/auth/server", () => ({
  isSignInEnabled: () => Promise.resolve(mocks.signIn),
}));
vi.mock("@/lib/billing/server", () => ({
  isBillingEnabled: () => Promise.resolve(mocks.billing),
}));
vi.mock("@/lib/flags/server", () => ({
  isFlagEnabled: (flag: string) =>
    Promise.resolve(flag === "webhook-subscriptions" && mocks.subscriptions),
}));
vi.mock("./_components/plan-overview", () => ({
  PlanOverview: ({
    billing,
    subscriptions,
  }: {
    billing: boolean;
    subscriptions: boolean;
  }) => (
    <p>{`plan overview, billing ${billing ? "on" : "off"}, subscriptions ${subscriptions ? "on" : "off"}`}</p>
  ),
}));

import DashboardPage, { metadata } from "./page";

async function renderPage() {
  return render(await DashboardPage());
}

describe("DashboardPage", () => {
  beforeEach(() => {
    mocks.signIn = true;
    mocks.billing = true;
    mocks.subscriptions = false;
    mocks.cookies = new Set(["wos-session"]);
    mocks.redirect.mockClear();
  });

  it("shows the dashboard shell with the plan overview", async () => {
    await renderPage();

    expect(metadata).toEqual({ title: "Dashboard" });
    expect(
      screen.getByRole("heading", { level: 1, name: "Dashboard" }),
    ).toBeVisible();
    expect(screen.getByRole("navigation", { name: "Dashboard" })).toBeVisible();
    expect(screen.getByRole("link", { name: "Billing" })).toBeVisible();
    expect(
      screen.getByText("plan overview, billing on, subscriptions off"),
    ).toBeVisible();
  });

  it("leaves billing out when billing is off", async () => {
    mocks.billing = false;

    await renderPage();

    expect(screen.queryByRole("link", { name: "Billing" })).toBeNull();
    expect(
      screen.getByText("plan overview, billing off, subscriptions off"),
    ).toBeVisible();
  });

  it("links to subscriptions when the flag is on", async () => {
    mocks.subscriptions = true;

    await renderPage();

    expect(screen.getByRole("link", { name: "Subscriptions" })).toHaveAttribute(
      "href",
      "/dashboard/subscriptions",
    );
    expect(
      screen.getByText("plan overview, billing on, subscriptions on"),
    ).toBeVisible();
  });

  it("sends a visitor without a session to sign in", async () => {
    mocks.cookies = new Set();

    await expect(renderPage()).rejects.toThrow(
      "redirect /auth/sign-in?returnTo=%2Fdashboard",
    );
  });

  it("is a 404 when sign-in is off", async () => {
    mocks.signIn = false;

    await expect(renderPage()).rejects.toThrow("not found");
    expect(mocks.redirect).not.toHaveBeenCalled();
  });
});
