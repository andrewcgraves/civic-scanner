import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  signOut: vi.fn(),
  isSignInEnabled: vi.fn(),
  getAuthConfig: vi.fn(),
  redirect: vi.fn((url: string) => {
    throw new Error(`redirect ${url}`);
  }),
}));

vi.mock("@workos-inc/authkit-nextjs", () => ({ signOut: mocks.signOut }));
vi.mock("next/navigation", () => ({ redirect: mocks.redirect }));
vi.mock("./config", () => ({ getAuthConfig: mocks.getAuthConfig }));
vi.mock("./server", () => ({ isSignInEnabled: mocks.isSignInEnabled }));

import { signOutAction } from "./actions";

describe("signOutAction", () => {
  beforeEach(() => {
    mocks.signOut.mockReset();
    mocks.isSignInEnabled.mockReset();
    mocks.redirect.mockClear();
    mocks.getAuthConfig.mockReturnValue({
      kind: "on",
      origin: "https://civic.example",
      redirectUri: "https://civic.example/auth/callback",
    });
  });

  it("ends the session through AuthKit and returns to the site root", async () => {
    mocks.isSignInEnabled.mockResolvedValue(true);
    mocks.signOut.mockResolvedValue(undefined);

    await signOutAction();

    expect(mocks.signOut).toHaveBeenCalledWith({
      returnTo: "https://civic.example/",
    });
    expect(mocks.redirect).not.toHaveBeenCalled();
  });

  it("only goes home without a valid configuration", async () => {
    mocks.getAuthConfig.mockReturnValue({ kind: "off" });

    await expect(signOutAction()).rejects.toThrow("redirect /");
    expect(mocks.signOut).not.toHaveBeenCalled();
    expect(mocks.isSignInEnabled).not.toHaveBeenCalled();
  });

  it("only goes home when sign-in is off", async () => {
    mocks.isSignInEnabled.mockResolvedValue(false);

    await expect(signOutAction()).rejects.toThrow("redirect /");
    expect(mocks.signOut).not.toHaveBeenCalled();
  });
});
