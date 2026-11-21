import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { UserApiClient, UserApiResult } from "@/lib/auth/api";

const mocks = vi.hoisted(() => {
  const failed = (): UserApiResult<unknown> => ({ status: "failed" });
  return {
    required: [] as string[],
    prompts: false,
    subscription: failed(),
    log: failed(),
    calls: [] as unknown[],
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
vi.mock("../server", () => ({
  requireSubscriptions: () => {
    mocks.required.push("subscriptions");
    return Promise.resolve();
  },
  loadAgencyChoices: () =>
    Promise.resolve({ ok: true, names: new Map([["la-metro", "LA Metro"]]) }),
  isDirectedPromptsEnabled: () => Promise.resolve(mocks.prompts),
}));
vi.mock("@/lib/auth/api", () => ({
  readApiAsUser: async (
    call: (client: UserApiClient) => Promise<unknown>,
  ): Promise<UserApiResult<unknown>> => {
    let path = "";
    const client = {
      GET: (requested: string, init: unknown) => {
        path = requested;
        mocks.calls.push({ path: requested, init });
        return Promise.resolve({ response: new Response() });
      },
    } as unknown as UserApiClient;
    await call(client);
    return path.endsWith("/deliveries") ? mocks.log : mocks.subscription;
  },
}));
vi.mock("../_components/send-test", () => ({
  SendTest: ({ id }: { id: string }) => <p>{`send a test to ${id}`}</p>,
}));
vi.mock("../_components/paused-notice", () => ({
  PausedNotice: ({
    agencyName,
    reason,
  }: {
    agencyName: string;
    reason: string | null;
  }) => <p>{`paused notice for ${agencyName}, ${String(reason)}`}</p>,
}));

const { default: SubscriptionPage, metadata } = await import("./page");

const ID = "8b0e6c43-3f3b-4f39-9a31-2a1f7c1a9e10";
const SUBSCRIPTION = {
  id: ID,
  agency: "la-metro",
  url: "https://hooks.example.org/civic",
  payloadVersion: "v1",
  format: "slack",
  status: "active",
  pausedReason: null,
  pausedAt: null,
  consecutiveFailedEvents: 0,
  createdAt: "2026-11-03T18:00:00Z",
  updatedAt: "2026-11-03T18:00:00Z",
  secretRotatedAt: "2026-11-03T18:00:00Z",
};
const DELIVERY = {
  eventId: "11111111-0000-4000-8000-000000000001",
  type: "agenda_published",
  meeting: {
    id: "22222222-0000-4000-8000-000000000001",
    title: "Board of Directors",
    startsAt: "2026-12-03T18:00:00Z",
  },
  succeeded: true,
  firstAttemptAt: "2026-11-17T17:38:00Z",
  lastAttemptAt: "2026-11-17T17:38:00Z",
  attempts: [
    {
      attempt: 1,
      startedAt: "2026-11-17T17:38:00Z",
      durationMs: 182,
      statusCode: 200,
      error: null,
      succeeded: true,
    },
  ],
};

async function renderPage(
  query: Record<string, string | string[] | undefined> = {},
  id = ID,
) {
  return render(
    await SubscriptionPage({
      params: Promise.resolve({ id }),
      searchParams: Promise.resolve(query),
    }),
  );
}

describe("SubscriptionPage", () => {
  beforeEach(() => {
    mocks.required = [];
    mocks.prompts = false;
    mocks.subscription = { status: "ok", data: SUBSCRIPTION };
    mocks.log = {
      status: "ok",
      data: { deliveries: [DELIVERY], nextCursor: null },
    };
    mocks.calls = [];
    mocks.notFound.mockClear();
    mocks.redirect.mockClear();
  });

  it("is titled and kept out of search results", () => {
    expect(metadata).toEqual({
      title: "Subscription",
      robots: { index: false },
    });
  });

  it("shows the subscription, a test form and its newest deliveries", async () => {
    await renderPage();

    expect(mocks.required).toEqual(["subscriptions"]);
    expect(mocks.calls).toEqual([
      {
        path: "/webhook-subscriptions/{id}",
        init: { params: { path: { id: ID } } },
      },
      {
        path: "/webhook-subscriptions/{id}/deliveries",
        init: { params: { path: { id: ID }, query: { limit: 20 } } },
      },
    ]);
    expect(
      screen.getByRole("heading", { level: 1, name: "LA Metro" }),
    ).toBeVisible();
    expect(
      screen.getByRole("navigation", { name: "Breadcrumb" }),
    ).toHaveTextContent("Dashboard/Subscriptions/LA Metro");
    expect(screen.getByText("Active")).toBeVisible();
    expect(screen.getByText("https://hooks.example.org/civic")).toBeVisible();
    expect(screen.getByRole("link", { name: "Edit LA Metro" })).toHaveAttribute(
      "href",
      `/dashboard/subscriptions/${ID}/edit`,
    );
    expect(screen.getByText(`send a test to ${ID}`)).toBeVisible();
    expect(screen.getByRole("link", { name: "Refresh" })).toHaveAttribute(
      "href",
      `/dashboard/subscriptions/${ID}`,
    );
    expect(screen.getByText("Agenda published")).toBeVisible();
    expect(screen.queryByRole("link", { name: "Older deliveries" })).toBeNull();
    expect(screen.queryByText(/paused notice/)).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("shows the whole directed prompt while directed prompts are on", async () => {
    mocks.prompts = true;
    mocks.subscription = {
      status: "ok",
      data: { ...SUBSCRIPTION, prompt: "Blue Line closures\nand delays" },
    };

    await renderPage();

    const section = screen.getByRole("region", { name: "Directed prompt" });
    expect(section).toHaveTextContent(/Meetings with nothing relevant/);
    expect(screen.getByText(/^Blue Line closures/)).toHaveClass(
      "whitespace-pre-wrap",
    );
    expect(
      screen.getByRole("link", { name: "Edit the prompt for LA Metro" }),
    ).toHaveAttribute("href", `/dashboard/subscriptions/${ID}/edit`);
    expect(
      screen.getByText(/A test does not use the directed prompt\./),
    ).toBeVisible();
  });

  it("shows the template of a custom subscription", async () => {
    mocks.subscription = {
      status: "ok",
      data: {
        ...SUBSCRIPTION,
        format: "custom",
        template: '{"title": "{{meeting.title}}"}',
      },
    };

    await renderPage();

    const section = screen.getByRole("region", { name: "Template" });
    expect(section).toHaveTextContent('{"title": "{{meeting.title}}"}');
    expect(
      screen.getByRole("link", { name: "Edit the template for LA Metro" }),
    ).toHaveAttribute("href", `/dashboard/subscriptions/${ID}/edit#template`);
  });

  it("has no template section for a preset, even with one saved", async () => {
    mocks.subscription = {
      status: "ok",
      data: { ...SUBSCRIPTION, format: "slack", template: "{}" },
    };

    await renderPage();

    expect(screen.queryByRole("region", { name: "Template" })).toBeNull();
  });

  it.each([
    ["off", false, "Fare changes"],
    ["on without a prompt", true, undefined],
    ["on with a blank prompt", true, "  "],
  ])(
    "has no prompt section with directed prompts %s",
    async (_case, prompts, prompt) => {
      mocks.prompts = prompts;
      mocks.subscription = {
        status: "ok",
        data: { ...SUBSCRIPTION, prompt },
      };

      await renderPage();

      expect(
        screen.queryByRole("region", { name: "Directed prompt" }),
      ).toBeNull();
      expect(screen.queryByText("Fare changes")).toBeNull();
      expect(screen.queryByText(/does not use the directed prompt/)).toBeNull();
    },
  );

  it("pages through older deliveries with the cursor", async () => {
    mocks.log = {
      status: "ok",
      data: { deliveries: [DELIVERY], nextCursor: "next+page" },
    };

    await renderPage({ cursor: "this-page" });

    expect(mocks.calls[1]).toEqual({
      path: "/webhook-subscriptions/{id}/deliveries",
      init: {
        params: { path: { id: ID }, query: { limit: 20, cursor: "this-page" } },
      },
    });
    expect(
      screen.getByRole("link", { name: "Older deliveries" }),
    ).toHaveAttribute(
      "href",
      `/dashboard/subscriptions/${ID}?cursor=next%2Bpage`,
    );
    expect(
      screen.getByRole("link", { name: "Back to the newest" }),
    ).toHaveAttribute("href", `/dashboard/subscriptions/${ID}`);
  });

  it("flags a paused subscription with its reason", async () => {
    mocks.subscription = {
      status: "ok",
      data: {
        ...SUBSCRIPTION,
        status: "paused",
        pausedReason: "failed_deliveries",
        pausedAt: "2026-11-18T17:40:00Z",
        consecutiveFailedEvents: 5,
      },
    };

    await renderPage();

    expect(screen.getByText("Paused: deliveries failing")).toBeVisible();
    expect(
      screen.getByText("paused notice for LA Metro, failed_deliveries"),
    ).toBeVisible();
    expect(screen.queryByText(/events failed to deliver/)).toBeNull();
  });

  it("warns about failures on an active subscription", async () => {
    mocks.subscription = {
      status: "ok",
      data: { ...SUBSCRIPTION, consecutiveFailedEvents: 3 },
    };

    await renderPage();

    expect(screen.getByRole("alert")).toHaveTextContent(
      "The last 3 events failed to deliver",
    );
  });

  it("confirms a resume", async () => {
    await renderPage({ resumed: "la-metro" });

    expect(screen.getByRole("status")).toHaveTextContent(
      "LA Metro is active again",
    );
  });

  it("ignores other outcomes", async () => {
    await renderPage({ paused: "la-metro" });

    expect(screen.queryByRole("status")).toBeNull();
  });

  it("offers the newest deliveries when the cursor is refused", async () => {
    mocks.log = { status: "rejected", httpStatus: 400, error: null };

    await renderPage({ cursor: "stale" });

    expect(screen.getByRole("alert")).toHaveTextContent(
      "That page of the log cannot be shown",
    );
    expect(
      screen.getByRole("link", { name: "Show the newest deliveries" }),
    ).toHaveAttribute("href", `/dashboard/subscriptions/${ID}`);
  });

  it("still shows the subscription when the log is unavailable", async () => {
    mocks.log = { status: "unavailable" };

    await renderPage();

    expect(screen.getByRole("alert")).toHaveTextContent(
      "The delivery log is unavailable right now",
    );
    expect(screen.getByText(`send a test to ${ID}`)).toBeVisible();
  });

  it("is not found for a malformed id, without calling the API", async () => {
    await expect(renderPage({}, "not-an-id")).rejects.toThrow("NEXT_NOT_FOUND");
    expect(mocks.calls).toEqual([]);
  });

  it("is not found when the API has no such subscription", async () => {
    mocks.subscription = { status: "not-found" };

    await expect(renderPage()).rejects.toThrow("NEXT_NOT_FOUND");
  });

  it.each<[UserApiResult<unknown>, string]>([
    [{ status: "signed-out" }, "/auth/sign-in"],
    [{ status: "payment-required" }, "/dashboard/subscriptions"],
  ])("redirects for %j", async (result, path) => {
    mocks.subscription = result;

    await expect(renderPage()).rejects.toThrow(`NEXT_REDIRECT ${path}`);
  });

  it("says when the subscription cannot be loaded", async () => {
    mocks.subscription = { status: "unavailable" };

    await renderPage();

    expect(
      screen.getByRole("heading", { level: 1, name: "Subscription" }),
    ).toBeVisible();
    expect(screen.getByRole("alert")).toHaveTextContent(
      "This subscription is unavailable right now",
    );
  });
});
