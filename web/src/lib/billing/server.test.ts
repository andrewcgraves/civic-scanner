import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  signIn: true,
  flags: ["billing"] as string[],
  notFound: vi.fn(() => {
    throw new Error("not found");
  }),
}));

vi.mock("next/navigation", () => ({ notFound: mocks.notFound }));
vi.mock("@/lib/auth/server", () => ({
  isSignInEnabled: () => Promise.resolve(mocks.signIn),
}));
vi.mock("@/lib/flags/server", () => ({
  isFlagEnabled: (flag: string) => Promise.resolve(mocks.flags.includes(flag)),
}));

import {
  BILLING_SIGN_IN_PATH,
  billingErrorPath,
  isBillingEnabled,
  readBillingError,
  requireBilling,
  stripeRedirectUrl,
} from "./server";

describe("billing server helpers", () => {
  beforeEach(() => {
    mocks.signIn = true;
    mocks.flags = ["billing"];
    mocks.notFound.mockClear();
  });

  it("is on only with sign-in and the billing flag", async () => {
    expect(await isBillingEnabled()).toBe(true);
    await expect(requireBilling()).resolves.toBeUndefined();

    mocks.flags = [];
    expect(await isBillingEnabled()).toBe(false);
    await expect(requireBilling()).rejects.toThrow("not found");

    mocks.flags = ["billing"];
    mocks.signIn = false;
    expect(await isBillingEnabled()).toBe(false);
    await expect(requireBilling()).rejects.toThrow("not found");
  });

  it("signs in back to the billing page", () => {
    expect(BILLING_SIGN_IN_PATH).toBe("/auth/sign-in?returnTo=%2Fbilling");
  });

  it("reads only known error codes", () => {
    expect(readBillingError("already-subscribed")).toBe("already-subscribed");
    expect(readBillingError("no-subscription")).toBe("no-subscription");
    expect(readBillingError("unavailable")).toBe("unavailable");
    for (const value of [
      undefined,
      "",
      "toString",
      "__proto__",
      ["unavailable"],
      "<script>",
    ]) {
      expect(readBillingError(value)).toBeNull();
    }
    expect(billingErrorPath("unavailable")).toBe("/billing?error=unavailable");
  });

  it("accepts only https URLs without credentials", () => {
    expect(stripeRedirectUrl("https://checkout.stripe.com/c/pay/cs_1#x")).toBe(
      "https://checkout.stripe.com/c/pay/cs_1#x",
    );
    for (const value of [
      "http://checkout.stripe.com/c/pay/cs_1",
      "javascript:alert(1)",
      "/billing",
      "//evil.example",
      "https://user:pass@checkout.stripe.com/",
      "https://user@checkout.stripe.com/",
      "not a url",
      "",
    ]) {
      expect(stripeRedirectUrl(value)).toBeNull();
    }
  });
});
