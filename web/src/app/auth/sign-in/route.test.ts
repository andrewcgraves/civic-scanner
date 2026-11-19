import { NextRequest } from "next/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type * as AuthServer from "@/lib/auth/server";

const mocks = vi.hoisted(() => ({
  getSignInUrl: vi.fn(),
  isSignInEnabled: vi.fn(),
}));

vi.mock("@workos-inc/authkit-nextjs", () => ({
  getSignInUrl: mocks.getSignInUrl,
}));
vi.mock("@/lib/auth/server", async (importOriginal) => ({
  ...(await importOriginal<typeof AuthServer>()),
  isSignInEnabled: mocks.isSignInEnabled,
}));

import { GET } from "./route";

const AUTHORIZE_URL =
  "https://api.workos.com/user_management/authorize?client_id=client_01TEST";

function signIn(query: string) {
  return GET(new NextRequest(`https://civic.example/auth/sign-in${query}`));
}

describe("GET /auth/sign-in", () => {
  beforeEach(() => {
    mocks.getSignInUrl.mockReset();
    mocks.getSignInUrl.mockResolvedValue(AUTHORIZE_URL);
    mocks.isSignInEnabled.mockReset();
    mocks.isSignInEnabled.mockResolvedValue(true);
  });

  it("is a 404 when sign-in is off", async () => {
    mocks.isSignInEnabled.mockResolvedValue(false);

    const response = await signIn("?returnTo=/agencies");

    expect(response.status).toBe(404);
    expect(mocks.getSignInUrl).not.toHaveBeenCalled();
  });

  it("redirects to the AuthKit authorization URL without caching", async () => {
    const response = await signIn("?returnTo=/agencies");

    expect(response.status).toBe(307);
    expect(response.headers.get("location")).toBe(AUTHORIZE_URL);
    expect(response.headers.get("cache-control")).toBe("no-store");
    expect(mocks.getSignInUrl).toHaveBeenCalledWith({ returnTo: "/agencies" });
  });

  it("returns home without a returnTo", async () => {
    await signIn("");

    expect(mocks.getSignInUrl).toHaveBeenCalledWith({ returnTo: "/" });
  });

  it.each([
    "https://evil.example/",
    "//evil.example/",
    "/\\evil.example",
    "javascript:alert(1)",
    "/auth/callback",
  ])("never returns to %s", async (returnTo) => {
    await signIn(`?returnTo=${encodeURIComponent(returnTo)}`);

    expect(mocks.getSignInUrl).toHaveBeenCalledWith({ returnTo: "/" });
  });
});
