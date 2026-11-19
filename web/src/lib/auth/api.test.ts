import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => {
  class TokenRefreshError extends Error {
    readonly isTransient: boolean;
    constructor(message: string, isTransient: boolean) {
      super(message);
      this.isTransient = isTransient;
    }
  }
  return {
    TokenRefreshError,
    withAuth: vi.fn(),
    refreshSession: vi.fn(),
    deleteCookie: vi.fn(),
    requestHeaders: new Headers(),
  };
});

vi.mock("@workos-inc/authkit-nextjs", () => ({
  TokenRefreshError: mocks.TokenRefreshError,
  withAuth: mocks.withAuth,
  refreshSession: mocks.refreshSession,
}));

vi.mock("next/headers", () => ({
  cookies: () => Promise.resolve({ delete: mocks.deleteCookie }),
  headers: () => Promise.resolve(mocks.requestHeaders),
}));

const ME = {
  id: "8b0e6c43-3f3b-4f39-9a31-2a1f7c1a9e10",
  email: "person@example.com",
  createdAt: "2026-11-01T00:00:00Z",
};

function apiResponse(status: number, body: unknown = undefined) {
  return () =>
    Promise.resolve(
      body === undefined
        ? new Response(null, { status })
        : Response.json(body, { status }),
    );
}

async function load(responses: (() => Promise<Response>)[]) {
  const queue = [...responses];
  const fetchMock = vi.fn<typeof fetch>(() => {
    const next = queue.shift();
    if (next === undefined) {
      throw new Error("unexpected request");
    }
    return next();
  });
  vi.stubGlobal("fetch", fetchMock);
  vi.resetModules();
  const api = await import("./api");
  return { ...api, fetchMock };
}

function sentAuthorization(fetchMock: ReturnType<typeof vi.fn>, index: number) {
  const [request, init] = fetchMock.mock.calls[index] as [Request, RequestInit];
  return {
    url: request.url,
    authorization: new Headers(init.headers).get("authorization"),
    init,
  };
}

describe("callApiAsUser", () => {
  beforeEach(() => {
    mocks.withAuth.mockReset();
    mocks.refreshSession.mockReset();
    mocks.deleteCookie.mockReset();
    mocks.requestHeaders = new Headers();
    vi.spyOn(console, "warn").mockImplementation(() => undefined);
    mocks.withAuth.mockResolvedValue({
      user: { id: "user_1" },
      accessToken: "token-1",
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.unstubAllEnvs();
    vi.restoreAllMocks();
  });

  it("calls the API with the session's access token", async () => {
    const { callApiAsUser, fetchMock } = await load([apiResponse(200, ME)]);

    const result = await callApiAsUser((api) => api.GET("/me"));

    expect(result).toEqual({ status: "ok", data: ME });
    const sent = sentAuthorization(fetchMock, 0);
    expect(sent.url).toBe("http://localhost:8080/v1/me");
    expect(sent.authorization).toBe("Bearer token-1");
    expect(sent.init.redirect).toBe("manual");
    expect(sent.init.cache).toBe("no-store");
    expect(mocks.refreshSession).not.toHaveBeenCalled();
  });

  it("is signed out without a session, and sends nothing", async () => {
    mocks.withAuth.mockResolvedValue({ user: null });
    const { callApiAsUser, fetchMock } = await load([]);

    expect(await callApiAsUser((api) => api.GET("/me"))).toEqual({
      status: "signed-out",
    });
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("refreshes once on a 401 and retries with the new token", async () => {
    mocks.refreshSession.mockResolvedValue({ accessToken: "token-2" });
    const { callApiAsUser, fetchMock } = await load([
      apiResponse(401, { code: "unauthorized", message: "x" }),
      apiResponse(200, ME),
    ]);

    expect(await callApiAsUser((api) => api.GET("/me"))).toEqual({
      status: "ok",
      data: ME,
    });
    expect(mocks.refreshSession).toHaveBeenCalledTimes(1);
    expect(sentAuthorization(fetchMock, 1).authorization).toBe(
      "Bearer token-2",
    );
    expect(mocks.deleteCookie).not.toHaveBeenCalled();
  });

  it("does not replay a refresh token the proxy has just rotated", async () => {
    mocks.requestHeaders = new Headers({
      "x-civic-session-state": "refreshed",
    });
    const { callApiAsUser, fetchMock } = await load([apiResponse(401)]);

    expect(await callApiAsUser((api) => api.GET("/me"))).toEqual({
      status: "signed-out",
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(mocks.refreshSession).not.toHaveBeenCalled();
    expect(mocks.deleteCookie).not.toHaveBeenCalled();
  });

  it("refreshes when the proxy only marked the session unverified", async () => {
    mocks.requestHeaders = new Headers({
      "x-civic-session-state": "unverified",
    });
    mocks.refreshSession.mockResolvedValue({ accessToken: "token-2" });
    const { callApiAsUser } = await load([
      apiResponse(401),
      apiResponse(200, ME),
    ]);

    expect(await callApiAsUser((api) => api.GET("/me"))).toEqual({
      status: "ok",
      data: ME,
    });
    expect(mocks.refreshSession).toHaveBeenCalledTimes(1);
  });

  it("is signed out after a second 401, and forgets the session", async () => {
    mocks.refreshSession.mockResolvedValue({ accessToken: "token-2" });
    const { callApiAsUser, fetchMock } = await load([
      apiResponse(401),
      apiResponse(401),
    ]);

    expect(await callApiAsUser((api) => api.GET("/me"))).toEqual({
      status: "signed-out",
    });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(mocks.refreshSession).toHaveBeenCalledTimes(1);
    expect(mocks.deleteCookie).toHaveBeenCalledWith({
      name: "wos-session",
      path: "/",
    });
  });

  it("forgets the session on the configured cookie domain", async () => {
    vi.stubEnv("WORKOS_COOKIE_DOMAIN", "civic.example");
    mocks.refreshSession.mockResolvedValue({ accessToken: "token-2" });
    const { callApiAsUser } = await load([apiResponse(401), apiResponse(401)]);

    await callApiAsUser((api) => api.GET("/me"));

    expect(mocks.deleteCookie).toHaveBeenCalledWith({
      name: "wos-session",
      path: "/",
      domain: "civic.example",
    });
  });

  it("is signed out when the refresh token is dead", async () => {
    mocks.refreshSession.mockRejectedValue(
      new mocks.TokenRefreshError("invalid_grant", false),
    );
    const { callApiAsUser, fetchMock } = await load([apiResponse(401)]);

    expect(await callApiAsUser((api) => api.GET("/me"))).toEqual({
      status: "signed-out",
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(mocks.deleteCookie).toHaveBeenCalled();
  });

  it("is signed out when the refresh finds no session", async () => {
    mocks.refreshSession.mockResolvedValue({ user: null });
    const { callApiAsUser } = await load([apiResponse(401)]);

    expect(await callApiAsUser((api) => api.GET("/me"))).toEqual({
      status: "signed-out",
    });
  });

  it("is signed out when the refresh fails unexpectedly", async () => {
    mocks.refreshSession.mockRejectedValue(new Error("boom"));
    const { callApiAsUser } = await load([apiResponse(401)]);

    expect(await callApiAsUser((api) => api.GET("/me"))).toEqual({
      status: "signed-out",
    });
  });

  it("keeps the session when the refresh fails transiently", async () => {
    mocks.refreshSession.mockRejectedValue(
      new mocks.TokenRefreshError("503", true),
    );
    const { callApiAsUser } = await load([apiResponse(401)]);

    expect(await callApiAsUser((api) => api.GET("/me"))).toEqual({
      status: "unavailable",
    });
    expect(mocks.deleteCookie).not.toHaveBeenCalled();
  });

  it("does not sign out or refresh on a 503", async () => {
    const { callApiAsUser } = await load([
      apiResponse(503, { code: "auth_unavailable", message: "x" }),
    ]);

    expect(await callApiAsUser((api) => api.GET("/me"))).toEqual({
      status: "unavailable",
    });
    expect(mocks.refreshSession).not.toHaveBeenCalled();
    expect(mocks.deleteCookie).not.toHaveBeenCalled();
  });

  it("reports a 404 as the feature being off", async () => {
    const { callApiAsUser } = await load([
      apiResponse(404, { code: "not_found", message: "x" }),
    ]);

    expect(await callApiAsUser((api) => api.GET("/me"))).toEqual({
      status: "not-found",
    });
    expect(mocks.refreshSession).not.toHaveBeenCalled();
  });

  it("reports a 404 after a refresh", async () => {
    mocks.refreshSession.mockResolvedValue({ accessToken: "token-2" });
    const { callApiAsUser } = await load([apiResponse(401), apiResponse(404)]);

    expect(await callApiAsUser((api) => api.GET("/me"))).toEqual({
      status: "not-found",
    });
  });

  it.each([
    ["a server error", apiResponse(500, { code: "internal", message: "x" })],
    ["a redirect", apiResponse(302)],
    ["a 200 without a body", apiResponse(200)],
    ["an unreachable API", () => Promise.reject(new TypeError("fetch failed"))],
  ])("fails on %s", async (_name, response) => {
    const { callApiAsUser } = await load([response]);

    expect(await callApiAsUser((api) => api.GET("/me"))).toEqual({
      status: "failed",
    });
    expect(mocks.deleteCookie).not.toHaveBeenCalled();
    expect(console.warn).toHaveBeenCalledTimes(1);
    expect(JSON.stringify(vi.mocked(console.warn).mock.calls)).not.toMatch(
      /token-1|localhost|Bearer/,
    );
  });

  it("logs a failed call by its error name only", async () => {
    const { callApiAsUser } = await load([
      () => Promise.reject(new TypeError("fetch failed for token-1")),
    ]);

    await callApiAsUser((api) => api.GET("/me"));

    expect(console.warn).toHaveBeenCalledWith(
      "API call as the user failed: TypeError",
    );
  });

  it("logs a failure that is not an Error", async () => {
    const { callApiAsUser } = await load([]);

    // eslint-disable-next-line @typescript-eslint/prefer-promise-reject-errors
    await callApiAsUser(() => Promise.reject("boom"));

    expect(console.warn).toHaveBeenCalledWith(
      "API call as the user failed: unknown error",
    );
  });

  it("never sends the token to another host", async () => {
    const { callApiAsUser, fetchMock } = await load([]);

    const result = await callApiAsUser((api) =>
      api.GET("/me", { baseUrl: "https://evil.example/v1" }),
    );

    expect(result).toEqual({ status: "failed" });
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("isApiUrl", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  async function loadWithBase(base: string) {
    vi.stubEnv("NEXT_PUBLIC_API_BASE_URL", base);
    vi.resetModules();
    return (await import("./api")).isApiUrl;
  }

  it.each([
    ["https://api.civic.example/v1/me", true],
    ["https://api.civic.example/v1", true],
    ["https://api.civic.example/v1/agencies/a/feed.xml", true],
    ["https://api.civic.example/v10/me", false],
    ["https://api.civic.example/me", false],
    ["http://api.civic.example/v1/me", false],
    ["https://api.civic.example:8443/v1/me", false],
    ["https://evil.example/v1/me", false],
    ["https://user:pass@api.civic.example/v1/me", false],
  ])("%s is %s under https://api.civic.example/v1", async (url, expected) => {
    const isApiUrl = await loadWithBase("https://api.civic.example/v1");

    expect(isApiUrl(url)).toBe(expected);
  });

  it("accepts any path under a base URL with a trailing slash", async () => {
    const isApiUrl = await loadWithBase("https://api.civic.example/");

    expect(isApiUrl("https://api.civic.example/v1/me")).toBe(true);
  });
});
