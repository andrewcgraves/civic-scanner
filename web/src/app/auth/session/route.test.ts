import { beforeEach, describe, expect, it, vi } from "vitest";

import type * as AuthServer from "@/lib/auth/server";

const mocks = vi.hoisted(() => ({
  withAuth: vi.fn(),
  callApiAsUser: vi.fn(),
  isSignInEnabled: vi.fn(),
  cookieNames: new Set<string>(),
  requestHeaders: new Headers(),
}));

vi.mock("@workos-inc/authkit-nextjs", () => ({ withAuth: mocks.withAuth }));
vi.mock("next/headers", () => ({
  cookies: () =>
    Promise.resolve({ has: (name: string) => mocks.cookieNames.has(name) }),
  headers: () => Promise.resolve(mocks.requestHeaders),
}));
vi.mock("@/lib/auth/api", () => ({ callApiAsUser: mocks.callApiAsUser }));
vi.mock("@/lib/auth/server", async (importOriginal) => ({
  ...(await importOriginal<typeof AuthServer>()),
  isSignInEnabled: mocks.isSignInEnabled,
}));

import { GET } from "./route";

const ACCESS_TOKEN = "eyJhbGciOiJSUzI1NiJ9.access.token";
const REFRESH_TOKEN = "refresh-token-secret";

describe("GET /auth/session", () => {
  beforeEach(() => {
    mocks.withAuth.mockReset();
    mocks.callApiAsUser.mockReset();
    mocks.isSignInEnabled.mockReset();
    mocks.isSignInEnabled.mockResolvedValue(true);
    mocks.cookieNames.clear();
    mocks.cookieNames.add("wos-session");
    mocks.requestHeaders = new Headers();
    mocks.withAuth.mockResolvedValue({
      user: { id: "user_1", email: "workos@example.com" },
      accessToken: ACCESS_TOKEN,
      refreshToken: REFRESH_TOKEN,
    });
  });

  async function session() {
    const response = await GET();
    const text = await response.text();
    return { response, text, body: JSON.parse(text) as unknown };
  }

  it("is a 404 when sign-in is off", async () => {
    mocks.isSignInEnabled.mockResolvedValue(false);

    const response = await GET();

    expect(response.status).toBe(404);
    expect(mocks.withAuth).not.toHaveBeenCalled();
  });

  it("is signed out without a session cookie, without reading the session", async () => {
    mocks.cookieNames.clear();

    const { response, body } = await session();

    expect(body).toEqual({ signedIn: false });
    expect(response.headers.get("cache-control")).toBe("private, no-store");
    expect(mocks.withAuth).not.toHaveBeenCalled();
    expect(mocks.callApiAsUser).not.toHaveBeenCalled();
  });

  it("is signed out when the session is not valid", async () => {
    mocks.withAuth.mockResolvedValue({ user: null });

    expect((await session()).body).toEqual({ signedIn: false });
    expect(mocks.callApiAsUser).not.toHaveBeenCalled();
  });

  it("stays signed in when the proxy kept a session it could not verify", async () => {
    mocks.withAuth.mockResolvedValue({ user: null });
    mocks.requestHeaders = new Headers({
      "x-civic-session-state": "unverified",
    });

    expect((await session()).body).toEqual({
      signedIn: true,
      email: null,
      role: null,
    });
    expect(mocks.callApiAsUser).not.toHaveBeenCalled();
  });

  it("shows the email the API has for the user", async () => {
    mocks.callApiAsUser.mockResolvedValue({
      status: "ok",
      data: { id: "u", email: "api@example.com", role: "user", createdAt: "x" },
    });

    const { response, body, text } = await session();

    expect(body).toEqual({
      signedIn: true,
      email: "api@example.com",
      role: "user",
    });
    expect(response.headers.get("vary")).toBe("Cookie");
    expect(text).not.toContain(ACCESS_TOKEN);
    expect(text).not.toContain(REFRESH_TOKEN);
    expect(text).not.toContain("workos@example.com");
  });

  it("calls GET /me through the authenticated client", async () => {
    mocks.callApiAsUser.mockResolvedValue({ status: "failed" });
    const get = vi.fn(() => Promise.resolve({ response: new Response() }));

    await session();
    const [call] = mocks.callApiAsUser.mock.calls[0] as [
      (api: { GET: typeof get }) => unknown,
    ];
    await call({ GET: get });

    expect(get).toHaveBeenCalledWith("/me");
  });

  it("has no email when the API has none", async () => {
    mocks.callApiAsUser.mockResolvedValue({
      status: "ok",
      data: { id: "u", email: null, role: "user", createdAt: "x" },
    });

    expect((await session()).body).toEqual({
      signedIn: true,
      email: null,
      role: "user",
    });
  });

  it("passes on the role the API has for the user", async () => {
    mocks.callApiAsUser.mockResolvedValue({
      status: "ok",
      data: { id: "u", email: null, role: "admin", createdAt: "x" },
    });

    expect((await session()).body).toEqual({
      signedIn: true,
      email: null,
      role: "admin",
    });
  });

  it("is signed out when the API rejects the token after a refresh", async () => {
    mocks.callApiAsUser.mockResolvedValue({ status: "signed-out" });

    expect((await session()).body).toEqual({ signedIn: false });
  });

  it.each([["unavailable"], ["not-found"], ["failed"]])(
    "stays signed in, without an email or role, when the API answers %s",
    async (status) => {
      mocks.callApiAsUser.mockResolvedValue({ status });

      expect((await session()).body).toEqual({
        signedIn: true,
        email: null,
        role: null,
      });
    },
  );
});
