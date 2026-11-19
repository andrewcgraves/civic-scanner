import { NextRequest, type NextFetchEvent } from "next/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  handleSession: vi.fn(),
  getAuthConfig: vi.fn(),
}));

vi.mock("@workos-inc/authkit-nextjs", () => ({
  authkitProxy: () => mocks.handleSession,
}));
vi.mock("@/lib/auth/config", () => ({ getAuthConfig: mocks.getAuthConfig }));

import { config, proxy } from "./proxy";

const event = {} as NextFetchEvent;

function request(cookie?: string) {
  return new NextRequest("https://civic.example/auth/session", {
    headers: cookie === undefined ? {} : { cookie },
  });
}

describe("proxy", () => {
  beforeEach(() => {
    mocks.handleSession.mockReset();
    mocks.handleSession.mockResolvedValue(new Response(null, { status: 200 }));
    mocks.getAuthConfig.mockReturnValue({
      kind: "on",
      origin: "https://civic.example",
      redirectUri: "https://civic.example/auth/callback",
    });
  });

  it("runs only for the session route", () => {
    expect(config).toEqual({ matcher: ["/auth/session"] });
  });

  it.each([[{ kind: "off" }], [{ kind: "invalid", problems: ["x"] }]])(
    "does no auth work without a valid configuration",
    (authConfig) => {
      mocks.getAuthConfig.mockReturnValue(authConfig);

      const response = proxy(request("wos-session=sealed"), event);

      expect(response).toBeInstanceOf(Response);
      expect(mocks.handleSession).not.toHaveBeenCalled();
    },
  );

  it("does no auth work without a session cookie", async () => {
    await proxy(request("other=1"), event);

    expect(mocks.handleSession).not.toHaveBeenCalled();
  });

  it("lets AuthKit read and refresh the session when there is one", async () => {
    const incoming = request("wos-session=sealed");

    const response = await proxy(incoming, event);

    expect(response?.status).toBe(200);
    expect(mocks.handleSession).toHaveBeenCalledWith(incoming, event);
  });
});
