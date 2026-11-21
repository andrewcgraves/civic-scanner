import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { UserApiClient, UserApiResult } from "@/lib/auth/api";

const mocks = vi.hoisted(() => {
  const failed = (): UserApiResult<unknown> => ({ status: "failed" });
  return {
    required: [] as string[],
    billing: true,
    prompts: false,
    result: failed(),
    names: new Map([["la-metro", "LA Metro"]]),
    calls: [] as string[],
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
vi.mock("./server", () => ({
  requireSubscriptions: () => {
    mocks.required.push("subscriptions");
    return Promise.resolve();
  },
  loadAgencyChoices: () => Promise.resolve({ ok: true, names: mocks.names }),
  isDirectedPromptsEnabled: () => Promise.resolve(mocks.prompts),
}));
vi.mock("@/lib/billing/server", () => ({
  isBillingEnabled: () => Promise.resolve(mocks.billing),
}));
vi.mock("@/lib/auth/api", () => ({
  readApiAsUser: async (
    call: (client: UserApiClient) => Promise<unknown>,
  ): Promise<UserApiResult<unknown>> => {
    const client = {
      GET: (path: string) => {
        mocks.calls.push(path);
        return Promise.resolve({ response: new Response() });
      },
    } as unknown as UserApiClient;
    await call(client);
    return mocks.result;
  },
}));
vi.mock("./_components/subscription-list", () => ({
  SubscriptionList: ({
    subscriptions,
    prompts,
  }: {
    subscriptions: unknown[];
    prompts: boolean;
  }) => (
    <p>{`${String(subscriptions.length)} subscriptions${prompts ? " with prompts" : ""}`}</p>
  ),
  AddButton: ({ count }: { count: number }) => (
    <p>{`add button at ${String(count)}`}</p>
  ),
}));
vi.mock("./_components/upgrade-prompt", () => ({
  UpgradePrompt: ({ billing }: { billing: boolean }) => (
    <p>{`upgrade prompt, billing ${billing ? "on" : "off"}`}</p>
  ),
}));

const { default: SubscriptionsPage, metadata } = await import("./page");

async function renderPage(
  params: Record<string, string | string[] | undefined> = {},
) {
  return render(
    await SubscriptionsPage({ searchParams: Promise.resolve(params) }),
  );
}

const LIST = { subscriptions: [{ id: "a" }, { id: "b" }] };

describe("SubscriptionsPage", () => {
  beforeEach(() => {
    mocks.required = [];
    mocks.billing = true;
    mocks.prompts = false;
    mocks.result = { status: "ok", data: LIST };
    mocks.calls = [];
    mocks.notFound.mockClear();
    mocks.redirect.mockClear();
  });

  it("is titled and kept out of search results", () => {
    expect(metadata).toEqual({
      title: "Subscriptions",
      robots: { index: false },
    });
  });

  it("lists the user's subscriptions under the dashboard tabs", async () => {
    await renderPage();

    expect(mocks.required).toEqual(["subscriptions"]);
    expect(mocks.calls).toEqual(["/webhook-subscriptions"]);
    expect(
      screen.getByRole("heading", { level: 1, name: "Dashboard" }),
    ).toBeVisible();
    expect(screen.getByRole("link", { name: "Subscriptions" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(screen.getByRole("link", { name: "Billing" })).toBeVisible();
    expect(
      screen.getByRole("heading", { level: 2, name: "Webhook subscriptions" }),
    ).toBeVisible();
    expect(screen.getByText("2 subscriptions")).toBeVisible();
    expect(screen.getByText("add button at 2")).toBeVisible();
    expect(screen.getByText(/signed request to your URL/)).toBeVisible();
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("shows prompts in the list while directed prompts are on", async () => {
    mocks.prompts = true;

    await renderPage();

    expect(screen.getByText("2 subscriptions with prompts")).toBeVisible();
  });

  it.each([
    [{ paused: "la-metro" }, "LA Metro is paused", /until you resume it/],
    [{ resumed: "la-metro" }, "LA Metro is active again", /from now on/],
    [{ updated: "la-metro" }, "LA Metro is saved", /new URL/],
    [{ deleted: "octa" }, "octa is deleted", /secret is gone/],
  ])("confirms %j", async (params, title, body) => {
    await renderPage(params);

    const status = screen.getByRole("status");
    expect(status).toHaveTextContent(title);
    expect(status).toHaveTextContent(body);
  });

  it("offers the paid plan to a free user", async () => {
    mocks.result = { status: "payment-required" };
    mocks.billing = false;

    await renderPage({ paused: "la-metro" });

    expect(screen.getByText("upgrade prompt, billing off")).toBeVisible();
    expect(screen.queryByText(/add button/)).toBeNull();
    expect(screen.queryByRole("status")).toBeNull();
    expect(screen.queryByRole("link", { name: "Billing" })).toBeNull();
  });

  it.each<UserApiResult<unknown>>([
    { status: "failed" },
    { status: "unavailable" },
    { status: "forbidden" },
  ])("explains that the list cannot be loaded on %j", async (result) => {
    mocks.result = result;

    await renderPage();

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Your subscriptions cannot be loaded right now",
    );
    expect(screen.getByRole("link", { name: "Try again" })).toHaveAttribute(
      "href",
      "/dashboard/subscriptions",
    );
    expect(screen.queryByText(/add button/)).toBeNull();
  });

  it("sends a signed-out user to sign in", async () => {
    mocks.result = { status: "signed-out" };

    await expect(renderPage()).rejects.toThrow(
      "NEXT_REDIRECT /auth/sign-in?returnTo=%2Fdashboard%2Fsubscriptions",
    );
  });

  it("is a 404 when the API has subscriptions off for the user", async () => {
    mocks.result = { status: "not-found" };

    await expect(renderPage()).rejects.toThrow("NEXT_NOT_FOUND");
  });
});
