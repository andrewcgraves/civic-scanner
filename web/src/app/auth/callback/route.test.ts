import { NextRequest } from "next/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type * as AuthServer from "@/lib/auth/server";

const mocks = vi.hoisted(() => ({
  handleAuth: vi.fn(),
  handler: vi.fn(),
  getAuthConfig: vi.fn(),
  isSignInEnabled: vi.fn(),
}));

vi.mock("@workos-inc/authkit-nextjs", () => ({
  handleAuth: mocks.handleAuth,
}));
vi.mock("@/lib/auth/config", () => ({ getAuthConfig: mocks.getAuthConfig }));
vi.mock("@/lib/auth/server", async (importOriginal) => ({
  ...(await importOriginal<typeof AuthServer>()),
  isSignInEnabled: mocks.isSignInEnabled,
}));

import { GET } from "./route";

const ON = {
  kind: "on",
  origin: "https://civic.example",
  redirectUri: "https://civic.example/auth/callback",
};

interface HandleAuthOptions {
  baseURL: string;
  returnPathname: string;
  onError: (context: { error: unknown; request: NextRequest }) => Response;
}

function callback() {
  return GET(
    new NextRequest("https://civic.example/auth/callback?code=c&state=s"),
  );
}

describe("GET /auth/callback", () => {
  beforeEach(() => {
    mocks.handleAuth.mockReset();
    mocks.handler.mockReset();
    mocks.handleAuth.mockReturnValue(mocks.handler);
    mocks.handler.mockResolvedValue(
      new Response(null, {
        status: 307,
        headers: { location: "https://civic.example/agencies" },
      }),
    );
    mocks.getAuthConfig.mockReturnValue(ON);
    mocks.isSignInEnabled.mockResolvedValue(true);
  });

  it.each([[{ kind: "off" }], [{ kind: "invalid", problems: ["x"] }]])(
    "is a 404 without a valid configuration",
    async (config) => {
      mocks.getAuthConfig.mockReturnValue(config);

      expect((await callback()).status).toBe(404);
      expect(mocks.handleAuth).not.toHaveBeenCalled();
    },
  );

  it("is a 404 when the accounts flag is off", async () => {
    mocks.isSignInEnabled.mockResolvedValue(false);

    expect((await callback()).status).toBe(404);
    expect(mocks.handleAuth).not.toHaveBeenCalled();
  });

  it("lets AuthKit check state and PKCE and return on the configured origin", async () => {
    const response = await callback();

    expect(response.status).toBe(307);
    expect(response.headers.get("location")).toBe(
      "https://civic.example/agencies",
    );
    const [options] = mocks.handleAuth.mock.calls[0] as [HandleAuthOptions];
    expect(options.baseURL).toBe("https://civic.example");
    expect(options.returnPathname).toBe("/");
    expect(mocks.handler).toHaveBeenCalledWith(expect.any(NextRequest));
  });

  it("answers a failed sign-in with a plain 400", async () => {
    await callback();
    const [options] = mocks.handleAuth.mock.calls[0] as [HandleAuthOptions];

    const response = options.onError({
      error: new Error("OAuth state mismatch"),
      request: new NextRequest("https://civic.example/auth/callback"),
    });

    expect(response.status).toBe(400);
    expect(response.headers.get("cache-control")).toBe("no-store");
    expect(await response.text()).toBe(
      "Sign-in failed. Go back and try again.\n",
    );
  });
});
