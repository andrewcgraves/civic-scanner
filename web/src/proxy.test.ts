import { NextRequest } from "next/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type * as AuthKit from "@workos-inc/authkit-nextjs";

const mocks = vi.hoisted(() => ({
  authkit: vi.fn(),
  getAuthConfig: vi.fn(),
}));

vi.mock("@workos-inc/authkit-nextjs", async (importOriginal) => ({
  ...(await importOriginal<typeof AuthKit>()),
  authkit: mocks.authkit,
}));
vi.mock("@/lib/auth/config", () => ({ getAuthConfig: mocks.getAuthConfig }));

import { config, proxy } from "./proxy";

const ON = {
  kind: "on",
  origin: "https://civic.example",
  redirectUri: "https://civic.example/auth/callback",
};

interface AuthkitOptions {
  redirectUri?: string;
  onSessionRefreshSuccess?: () => void;
}

function request(headers: Record<string, string> = {}) {
  return new NextRequest("https://civic.example/auth/session", { headers });
}

function forwarded(response: Response, name: string): string | null {
  return response.headers.get(`x-middleware-request-${name}`);
}

interface Answer {
  user?: { id: string } | null;
  setCookies?: string[];
  refresh?: boolean;
}

function answer({
  user = { id: "user_01" },
  setCookies = [],
  refresh = false,
}: Answer = {}) {
  mocks.authkit.mockImplementation(
    (_request: unknown, options: AuthkitOptions) => {
      if (refresh) {
        options.onSessionRefreshSuccess?.();
      }
      const headers = new Headers({
        "x-workos-middleware": "true",
        "x-workos-session": "sealed-session",
      });
      for (const cookie of setCookies) {
        headers.append("set-cookie", cookie);
      }
      return Promise.resolve({ session: { user }, headers });
    },
  );
}

describe("proxy", () => {
  beforeEach(() => {
    mocks.authkit.mockReset();
    mocks.getAuthConfig.mockReturnValue(ON);
    answer();
  });

  it("runs only for the session route and the agency submission page", () => {
    expect(config).toEqual({ matcher: ["/auth/session", "/submit-agency"] });
  });

  it.each([[{ kind: "off" }], [{ kind: "invalid", problems: ["x"] }]])(
    "does no auth work without a valid configuration",
    async (authConfig) => {
      mocks.getAuthConfig.mockReturnValue(authConfig);

      const response = await proxy(
        request({
          cookie: "wos-session=sealed",
          "x-civic-session-state": "unverified",
        }),
      );

      expect(mocks.authkit).not.toHaveBeenCalled();
      expect(forwarded(response, "x-civic-session-state")).toBeNull();
    },
  );

  it("does no auth work without a session cookie", async () => {
    const response = await proxy(
      request({ cookie: "other=1", "x-civic-session-state": "refreshed" }),
    );

    expect(mocks.authkit).not.toHaveBeenCalled();
    expect(forwarded(response, "x-civic-session-state")).toBeNull();
  });

  it("reads the session with the configured redirect URI and forwards it to the route only", async () => {
    const incoming = request({
      cookie: "wos-session=sealed",
      "x-redirect-uri": "https://evil.example/cb",
      "x-civic-session-state": "refreshed",
    });

    const response = await proxy(incoming);

    const [passed, options] = mocks.authkit.mock.calls[0] as [
      NextRequest,
      AuthkitOptions,
    ];
    expect(passed).toBe(incoming);
    expect(options.redirectUri).toBe(ON.redirectUri);
    expect(forwarded(response, "x-workos-session")).toBe("sealed-session");
    expect(forwarded(response, "x-redirect-uri")).toBeNull();
    expect(forwarded(response, "x-civic-session-state")).toBeNull();
    expect(response.headers.get("x-workos-session")).toBeNull();
  });

  it("marks a session the proxy refreshed, with one session cookie", async () => {
    answer({
      refresh: true,
      setCookies: ["wos-session=new-sealed; Path=/; HttpOnly; SameSite=Lax"],
    });

    const response = await proxy(request({ cookie: "wos-session=sealed" }));

    expect(forwarded(response, "x-civic-session-state")).toBe("refreshed");
    expect(response.headers.getSetCookie()).toEqual([
      "wos-session=new-sealed; Path=/; HttpOnly; SameSite=Lax",
    ]);
  });

  it("marks a session kept without a verified user as unverified", async () => {
    answer({ user: null });

    const response = await proxy(request({ cookie: "wos-session=sealed" }));

    expect(forwarded(response, "x-civic-session-state")).toBe("unverified");
  });

  it("does not mark a session that AuthKit cleared", async () => {
    answer({
      user: null,
      setCookies: ["wos-session=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0"],
    });

    const response = await proxy(request({ cookie: "wos-session=sealed" }));

    expect(forwarded(response, "x-civic-session-state")).toBeNull();
    expect(response.headers.getSetCookie()).toHaveLength(1);
  });

  it("never sets a sign-in verifier cookie on the session route", async () => {
    answer({
      user: null,
      setCookies: [
        "wos-auth-verifier-abc=sealed; Path=/; HttpOnly; Max-Age=600",
        "wos-session=; Path=/; Max-Age=0",
      ],
    });

    const response = await proxy(request({ cookie: "wos-session=sealed" }));

    expect(response.headers.getSetCookie()).toEqual([
      "wos-session=; Path=/; Max-Age=0",
    ]);
  });
});
