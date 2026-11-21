import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { UserApiClient, UserApiResult } from "@/lib/auth/api";

const mocks = vi.hoisted(() => {
  const failed = (): UserApiResult<unknown> => ({ status: "failed" });
  return {
    required: [] as string[],
    prompts: false,
    result: failed(),
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
vi.mock("../../server", () => ({
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
    const client = {
      GET: (path: string, init: unknown) => {
        mocks.calls.push({ path, init });
        return Promise.resolve({ response: new Response() });
      },
    } as unknown as UserApiClient;
    await call(client);
    return mocks.result;
  },
}));
vi.mock("../../_components/subscription-form", () => ({
  SubscriptionForm: ({
    mode,
    prompts,
  }: {
    mode: unknown;
    prompts: boolean;
  }) => (
    <p>{`form${prompts ? " with a prompt" : ""} ${JSON.stringify(mode)}`}</p>
  ),
}));

const { default: EditSubscriptionPage, metadata } = await import("./page");

const ID = "8b0e6c43-3f3b-4f39-9a31-2a1f7c1a9e10";
const SUBSCRIPTION = {
  id: ID,
  agency: "la-metro",
  url: "https://hooks.example.org/civic",
  format: "slack",
};

async function renderPage(id = ID) {
  return render(
    await EditSubscriptionPage({ params: Promise.resolve({ id }) }),
  );
}

describe("EditSubscriptionPage", () => {
  beforeEach(() => {
    mocks.required = [];
    mocks.prompts = false;
    mocks.result = { status: "ok", data: SUBSCRIPTION };
    mocks.calls = [];
    mocks.notFound.mockClear();
    mocks.redirect.mockClear();
  });

  it("is titled and kept out of search results", () => {
    expect(metadata).toEqual({
      title: "Edit a subscription",
      robots: { index: false },
    });
  });

  it("loads the subscription and edits it by agency name", async () => {
    await renderPage();

    expect(mocks.required).toEqual(["subscriptions"]);
    expect(mocks.calls).toEqual([
      {
        path: "/webhook-subscriptions/{id}",
        init: { params: { path: { id: ID } } },
      },
    ]);
    expect(
      screen.getByRole("heading", { level: 1, name: "Edit LA Metro" }),
    ).toBeVisible();
    expect(
      screen.getByText(
        `form ${JSON.stringify({ kind: "edit", id: ID, agency: "la-metro", agencyName: "LA Metro", url: SUBSCRIPTION.url, format: "slack", prompt: "" })}`,
      ),
    ).toBeVisible();
  });

  it("hands the saved prompt to the form while directed prompts are on", async () => {
    mocks.prompts = true;
    mocks.result = {
      status: "ok",
      data: { ...SUBSCRIPTION, prompt: "Fare changes" },
    };

    await renderPage();

    expect(
      screen.getByText(/^form with a prompt .*"prompt":"Fare changes"\}$/),
    ).toBeVisible();
  });

  it("is a 404 for an id that is not a UUID, without calling the API", async () => {
    await expect(renderPage("nope")).rejects.toThrow("NEXT_NOT_FOUND");
    expect(mocks.calls).toEqual([]);
  });

  it("is a 404 for a subscription that does not exist", async () => {
    mocks.result = { status: "not-found" };

    await expect(renderPage()).rejects.toThrow("NEXT_NOT_FOUND");
  });

  it("sends a signed-out user to sign in", async () => {
    mocks.result = { status: "signed-out" };

    await expect(renderPage()).rejects.toThrow(
      "NEXT_REDIRECT /auth/sign-in?returnTo=%2Fdashboard%2Fsubscriptions",
    );
  });

  it("sends a free user back to the list, which offers the paid plan", async () => {
    mocks.result = { status: "payment-required" };

    await expect(renderPage()).rejects.toThrow(
      "NEXT_REDIRECT /dashboard/subscriptions",
    );
  });

  it("explains when the subscription cannot be loaded", async () => {
    mocks.result = { status: "failed" };

    await renderPage();

    expect(
      screen.getByRole("heading", { level: 1, name: "Edit a subscription" }),
    ).toBeVisible();
    expect(screen.getByRole("alert")).toHaveTextContent(
      "This subscription is unavailable right now",
    );
  });
});
