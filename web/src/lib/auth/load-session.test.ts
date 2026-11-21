import { afterEach, describe, expect, it, vi } from "vitest";

import { loadSessionState } from "./load-session";

function stubFetch(response: () => Promise<Response>) {
  const fetchMock = vi.fn<typeof fetch>(() => response());
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("loadSessionState", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("asks the server for the session without caching", async () => {
    const fetchMock = stubFetch(() =>
      Promise.resolve(Response.json({ signedIn: false })),
    );

    expect(await loadSessionState()).toEqual({ signedIn: false });
    expect(fetchMock).toHaveBeenCalledWith("/auth/session", {
      cache: "no-store",
      headers: { accept: "application/json" },
    });
  });

  it("reads a signed-in session with its plan", async () => {
    stubFetch(() =>
      Promise.resolve(
        Response.json({ signedIn: true, email: null, plan: "paid" }),
      ),
    );

    expect(await loadSessionState()).toEqual({
      signedIn: true,
      email: null,
      role: null,
      plan: "paid",
    });
  });

  it.each([
    [
      "an error status",
      () => Promise.resolve(new Response(null, { status: 404 })),
    ],
    ["an unexpected body", () => Promise.resolve(Response.json({ user: "x" }))],
    ["a body that is not JSON", () => Promise.resolve(new Response("<html>"))],
    ["a network error", () => Promise.reject(new TypeError("Failed to fetch"))],
  ])("is null on %s", async (_name, response) => {
    stubFetch(response);

    expect(await loadSessionState()).toBeNull();
  });
});
