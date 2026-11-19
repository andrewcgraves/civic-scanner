import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  signOut: vi.fn(),
  isSignInEnabled: vi.fn(),
  redirect: vi.fn((url: string) => {
    throw new Error(`redirect ${url}`);
  }),
}));

vi.mock("@workos-inc/authkit-nextjs", () => ({ signOut: mocks.signOut }));
vi.mock("next/navigation", () => ({ redirect: mocks.redirect }));
vi.mock("./server", () => ({ isSignInEnabled: mocks.isSignInEnabled }));

import { signOutAction } from "./actions";

describe("signOutAction", () => {
  beforeEach(() => {
    mocks.signOut.mockReset();
    mocks.isSignInEnabled.mockReset();
    mocks.redirect.mockClear();
  });

  it("ends the session through AuthKit", async () => {
    mocks.isSignInEnabled.mockResolvedValue(true);
    mocks.signOut.mockResolvedValue(undefined);

    await signOutAction();

    expect(mocks.signOut).toHaveBeenCalledWith();
    expect(mocks.redirect).not.toHaveBeenCalled();
  });

  it("only goes home when sign-in is off", async () => {
    mocks.isSignInEnabled.mockResolvedValue(false);

    await expect(signOutAction()).rejects.toThrow("redirect /");
    expect(mocks.signOut).not.toHaveBeenCalled();
  });
});
