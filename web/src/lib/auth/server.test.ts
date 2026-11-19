import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  getAuthConfig: vi.fn(),
  isFlagEnabled: vi.fn(),
}));

vi.mock("./config", () => ({ getAuthConfig: mocks.getAuthConfig }));
vi.mock("@/lib/flags/server", () => ({ isFlagEnabled: mocks.isFlagEnabled }));

import { isSignInEnabled, notFound } from "./server";

const ON = {
  kind: "on",
  origin: "https://civic.example",
  redirectUri: "https://civic.example/auth/callback",
};

describe("isSignInEnabled", () => {
  beforeEach(() => {
    mocks.getAuthConfig.mockReset();
    mocks.isFlagEnabled.mockReset();
  });

  it("is on when WorkOS is configured and the accounts flag is on", async () => {
    mocks.getAuthConfig.mockReturnValue(ON);
    mocks.isFlagEnabled.mockResolvedValue(true);

    expect(await isSignInEnabled()).toBe(true);
    expect(mocks.isFlagEnabled).toHaveBeenCalledWith("accounts");
  });

  it("is off when the accounts flag is off", async () => {
    mocks.getAuthConfig.mockReturnValue(ON);
    mocks.isFlagEnabled.mockResolvedValue(false);

    expect(await isSignInEnabled()).toBe(false);
  });

  it.each([[{ kind: "off" }], [{ kind: "invalid", problems: ["x"] }]])(
    "is off without a valid WorkOS configuration, without checking the flag",
    async (config) => {
      mocks.getAuthConfig.mockReturnValue(config);

      expect(await isSignInEnabled()).toBe(false);
      expect(mocks.isFlagEnabled).not.toHaveBeenCalled();
    },
  );
});

describe("notFound", () => {
  it("is an uncached plain 404", async () => {
    const response = notFound();

    expect(response.status).toBe(404);
    expect(response.headers.get("cache-control")).toBe("no-store");
    expect(await response.text()).toBe("Not found\n");
  });
});
