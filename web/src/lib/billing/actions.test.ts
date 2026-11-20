import { beforeEach, describe, expect, it, vi } from "vitest";

import type { UserApiClient, UserApiResult } from "@/lib/auth/api";

type Result = UserApiResult<{ url: string }>;

const mocks = vi.hoisted(() => ({
  enabled: true,
  result: ((): Result => ({ status: "failed" }))(),
  paths: [] as string[],
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
vi.mock("@/lib/auth/api", () => ({
  callApiAsUser: async (
    call: (client: UserApiClient) => Promise<unknown>,
  ): Promise<unknown> => {
    const client = {
      POST: (path: string) => {
        mocks.paths.push(path);
        return Promise.resolve({ response: new Response() });
      },
    } as unknown as UserApiClient;
    await call(client);
    return mocks.result;
  },
}));
vi.mock("./server", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  isBillingEnabled: () => Promise.resolve(mocks.enabled),
}));

import { openBillingPortalAction, startCheckoutAction } from "./actions";

const actions = [
  ["startCheckoutAction", startCheckoutAction, "/billing/checkout-session"],
  [
    "openBillingPortalAction",
    openBillingPortalAction,
    "/billing/portal-session",
  ],
] as const;

describe.each(actions)("%s", (_name, action, apiPath) => {
  beforeEach(() => {
    mocks.enabled = true;
    mocks.paths = [];
    mocks.redirect.mockClear();
    mocks.notFound.mockClear();
  });

  it("redirects to the URL the API returns", async () => {
    mocks.result = {
      status: "ok",
      data: { url: "https://checkout.stripe.com/c/pay/cs_test_1" },
    };

    await expect(action()).rejects.toThrow(
      "redirect https://checkout.stripe.com/c/pay/cs_test_1",
    );
    expect(mocks.paths).toEqual([apiPath]);
  });

  it("refuses a URL that is not https", async () => {
    mocks.result = { status: "ok", data: { url: "javascript:alert(1)" } };

    await expect(action()).rejects.toThrow(
      "redirect /billing?error=unavailable",
    );
  });

  it("sends a signed-out user to sign in and back", async () => {
    mocks.result = { status: "signed-out" };

    await expect(action()).rejects.toThrow(
      "redirect /auth/sign-in?returnTo=%2Fbilling",
    );
  });

  it.each([["unavailable"], ["failed"], ["not-found"]] as const)(
    "shows that billing is unavailable on %s",
    async (status) => {
      mocks.result = { status };

      await expect(action()).rejects.toThrow(
        "redirect /billing?error=unavailable",
      );
    },
  );

  it("is a 404 without calling the API when billing is off", async () => {
    mocks.enabled = false;

    await expect(action()).rejects.toThrow("not found");
    expect(mocks.paths).toEqual([]);
    expect(mocks.redirect).not.toHaveBeenCalled();
  });
});

describe("conflicts", () => {
  beforeEach(() => {
    mocks.enabled = true;
    mocks.result = { status: "conflict", error: {} };
  });

  it("tells a subscribed user to use the portal", async () => {
    await expect(startCheckoutAction()).rejects.toThrow(
      "redirect /billing?error=already-subscribed",
    );
  });

  it("tells a user without a customer to start the plan", async () => {
    await expect(openBillingPortalAction()).rejects.toThrow(
      "redirect /billing?error=no-subscription",
    );
  });
});
