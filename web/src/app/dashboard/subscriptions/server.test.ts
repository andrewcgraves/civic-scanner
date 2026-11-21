import { beforeEach, describe, expect, it, vi } from "vitest";

import type { Agency } from "@/lib/api/client";
import type { Loaded } from "@/lib/api/load";

const mocks = vi.hoisted(() => {
  const unloaded = (): Loaded<Agency[]> => ({ ok: false });
  return {
    signIn: true,
    flag: true,
    hasCookie: true,
    agencies: unloaded(),
    flagsAsked: [] as string[],
    notFound: vi.fn(() => {
      throw new Error("NEXT_NOT_FOUND");
    }),
    redirect: vi.fn((path: string) => {
      throw new Error(`NEXT_REDIRECT ${path}`);
    }),
  };
});

vi.mock("next/navigation", () => ({
  notFound: mocks.notFound,
  redirect: mocks.redirect,
}));
vi.mock("next/headers", () => ({
  cookies: () =>
    Promise.resolve({
      has: (name: string) => mocks.hasCookie && name === "wos-session",
    }),
}));
vi.mock("@/lib/auth/server", () => ({
  isSignInEnabled: () => Promise.resolve(mocks.signIn),
}));
vi.mock("@/lib/flags/server", () => ({
  isFlagEnabled: (flag: string) => {
    mocks.flagsAsked.push(flag);
    return Promise.resolve(mocks.flag);
  },
}));
vi.mock("@/lib/api/load", () => ({
  loadAgencies: () => Promise.resolve(mocks.agencies),
}));

const { isSubscriptionsEnabled, loadAgencyChoices, requireSubscriptions } =
  await import("./server");

describe("isSubscriptionsEnabled", () => {
  beforeEach(() => {
    mocks.signIn = true;
    mocks.flag = true;
    mocks.flagsAsked = [];
  });

  it("needs sign-in and the webhook-subscriptions flag", async () => {
    expect(await isSubscriptionsEnabled()).toBe(true);
    expect(mocks.flagsAsked).toEqual(["webhook-subscriptions"]);

    mocks.flag = false;
    expect(await isSubscriptionsEnabled()).toBe(false);
  });

  it("does not ask for the flag when sign-in is off", async () => {
    mocks.signIn = false;

    expect(await isSubscriptionsEnabled()).toBe(false);
    expect(mocks.flagsAsked).toEqual([]);
  });
});

describe("requireSubscriptions", () => {
  beforeEach(() => {
    mocks.signIn = true;
    mocks.flag = true;
    mocks.hasCookie = true;
    mocks.redirect.mockClear();
  });

  it("passes a signed-in visitor when subscriptions are on", async () => {
    await expect(requireSubscriptions()).resolves.toBeUndefined();
  });

  it("is a 404 when subscriptions are off", async () => {
    mocks.flag = false;

    await expect(requireSubscriptions()).rejects.toThrow("NEXT_NOT_FOUND");
    expect(mocks.redirect).not.toHaveBeenCalled();
  });

  it("sends a visitor without a session to sign in", async () => {
    mocks.hasCookie = false;

    await expect(requireSubscriptions()).rejects.toThrow(
      "NEXT_REDIRECT /auth/sign-in?returnTo=%2Fdashboard%2Fsubscriptions",
    );
  });
});

describe("loadAgencyChoices", () => {
  it("lists the agencies and maps slugs to names", async () => {
    const agencies = [
      {
        id: "a",
        slug: "la-metro",
        name: "LA Metro",
        website: "https://www.metro.net",
      },
      {
        id: "b",
        slug: "octa",
        name: "Orange County Transportation Authority",
        website: "https://www.octa.net",
      },
    ];
    mocks.agencies = { ok: true, data: agencies };

    const choices = await loadAgencyChoices();

    expect(choices).toEqual({
      ok: true,
      agencies,
      names: new Map([
        ["la-metro", "LA Metro"],
        ["octa", "Orange County Transportation Authority"],
      ]),
    });
  });

  it("has no names when the agencies cannot be loaded", async () => {
    mocks.agencies = { ok: false };

    expect(await loadAgencyChoices()).toEqual({ ok: false, names: new Map() });
  });
});
