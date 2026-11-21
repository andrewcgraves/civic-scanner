import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  signIn: true,
  billing: true,
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
vi.mock("./_components/plan-overview", () => ({
  PlanOverview: ({ billing }: { billing: boolean }) => (
    <p>{`plan overview, billing ${billing ? "on" : "off"}`}</p>
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
    expect(screen.getByText("plan overview, billing on")).toBeVisible();
  });

  it("leaves billing out when billing is off", async () => {
    mocks.billing = false;

    await renderPage();

    expect(screen.queryByRole("link", { name: "Billing" })).toBeNull();
    expect(screen.getByText("plan overview, billing off")).toBeVisible();
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
