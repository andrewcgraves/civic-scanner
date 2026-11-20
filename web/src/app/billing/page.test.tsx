import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  enabled: true,
  cookies: new Set<string>(["wos-session"]),
  startCheckoutAction: vi.fn(),
  openBillingPortalAction: vi.fn(),
  redirect: vi.fn((url: string) => {
    throw new Error(`redirect ${url}`);
  }),
}));

vi.mock("next/navigation", () => ({ redirect: mocks.redirect }));
vi.mock("next/headers", () => ({
  cookies: () =>
    Promise.resolve({ has: (name: string) => mocks.cookies.has(name) }),
}));
vi.mock("@/lib/billing/actions", () => ({
  startCheckoutAction: mocks.startCheckoutAction,
  openBillingPortalAction: mocks.openBillingPortalAction,
}));
vi.mock("@/lib/billing/server", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  requireBilling: () =>
    mocks.enabled ? Promise.resolve() : Promise.reject(new Error("not found")),
}));

import BillingPage, { metadata } from "./page";

function renderPage(query: Record<string, string | string[] | undefined> = {}) {
  return BillingPage({ searchParams: Promise.resolve(query) }).then((page) =>
    render(page),
  );
}

describe("BillingPage", () => {
  beforeEach(() => {
    mocks.enabled = true;
    mocks.cookies = new Set(["wos-session"]);
    mocks.redirect.mockClear();
  });

  it("offers checkout and the billing portal as form posts", async () => {
    await renderPage();

    expect(metadata).toEqual({ title: "Billing" });
    expect(
      screen.getByRole("heading", { level: 1, name: "Billing" }),
    ).toBeVisible();
    const start = screen.getByRole("button", { name: "Start the paid plan" });
    const portal = screen.getByRole("button", {
      name: "Open the billing portal",
    });
    expect(start).toHaveAttribute("type", "submit");
    expect(start.closest("form")).not.toBeNull();
    expect(portal.closest("form")).not.toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it.each([
    ["already-subscribed", "You already have a subscription."],
    ["no-subscription", "There is no billing account yet."],
    ["unavailable", "Billing is unavailable right now."],
  ])("explains the %s error", async (error, text) => {
    await renderPage({ error });

    expect(screen.getByRole("alert")).toHaveTextContent(text);
  });

  it.each([["something-else"], [["unavailable", "x"]]])(
    "ignores an unknown error %j",
    async (error) => {
      await renderPage({ error });

      expect(screen.queryByRole("alert")).toBeNull();
    },
  );

  it("sends a visitor without a session to sign in", async () => {
    mocks.cookies = new Set();

    await expect(renderPage()).rejects.toThrow(
      "redirect /auth/sign-in?returnTo=%2Fbilling",
    );
  });

  it("is a 404 when billing is off", async () => {
    mocks.enabled = false;

    await expect(renderPage()).rejects.toThrow("not found");
    expect(mocks.redirect).not.toHaveBeenCalled();
  });
});
