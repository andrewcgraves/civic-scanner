import { afterEach, describe, expect, it, vi } from "vitest";

const feedXml =
  '<?xml version="1.0" encoding="UTF-8"?>\n<rss version="2.0"></rss>\n';

function upstreamFeed(init: ResponseInit & { body?: string | null } = {}) {
  const { body = feedXml, ...rest } = init;
  return new Response(body, {
    status: 200,
    headers: {
      "content-type": "application/rss+xml; charset=utf-8",
      etag: '"abc"',
      "cache-control": "public, max-age=300",
      "set-cookie": "session=secret",
      "x-internal": "yes",
    },
    ...rest,
  });
}

async function call(
  method: "GET" | "HEAD",
  slug: string,
  upstream: () => Promise<Response>,
  headers: HeadersInit = {},
) {
  const fetchMock = vi.fn<typeof fetch>(() => upstream());
  vi.stubGlobal("fetch", fetchMock);
  vi.resetModules();
  const route = await import("./route");
  const request = new Request(
    `https://civic.example/agencies/${slug}/feed.xml`,
    { method, headers },
  );
  const response = await route[method](request, {
    params: Promise.resolve({ slug }),
  });
  return { response, fetchMock };
}

describe("agency feed route", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("serves the API's feed with its validators", async () => {
    const { response, fetchMock } = await call(
      "GET",
      "la-metro",
      () => Promise.resolve(upstreamFeed()),
      {
        "if-none-match": '"old"',
        authorization: "Bearer token",
        cookie: "a=b",
        "x-forwarded-for": "1.2.3.4",
      },
    );

    expect(response.status).toBe(200);
    expect(await response.text()).toBe(feedXml);
    expect(response.headers.get("content-type")).toBe(
      "application/rss+xml; charset=utf-8",
    );
    expect(response.headers.get("etag")).toBe('"abc"');
    expect(response.headers.get("cache-control")).toBe("public, max-age=300");
    expect(response.headers.get("set-cookie")).toBeNull();
    expect(response.headers.get("x-internal")).toBeNull();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] ?? [];
    expect(url).toBe("http://localhost:8080/v1/agencies/la-metro/feed.xml");
    expect(init?.method).toBe("GET");
    expect(init?.redirect).toBe("manual");
    const sent = new Headers(init?.headers);
    expect([...sent.keys()]).toEqual(["if-none-match"]);
    expect(sent.get("if-none-match")).toBe('"old"');
  });

  it("does not send If-None-Match when the reader sent none", async () => {
    const { fetchMock } = await call("GET", "octa", () =>
      Promise.resolve(upstreamFeed()),
    );
    const [, init] = fetchMock.mock.calls[0] ?? [];
    expect([...new Headers(init?.headers).keys()]).toEqual([]);
  });

  it("passes a 304 through without a body", async () => {
    const { response } = await call(
      "GET",
      "la-metro",
      () => Promise.resolve(upstreamFeed({ status: 304, body: null })),
      { "if-none-match": '"abc"' },
    );
    expect(response.status).toBe(304);
    expect(response.body).toBeNull();
    expect(response.headers.get("etag")).toBe('"abc"');
    expect(response.headers.get("cache-control")).toBe("public, max-age=300");
  });

  it("answers HEAD with the API's headers and no body", async () => {
    const { response, fetchMock } = await call("HEAD", "la-metro", () =>
      Promise.resolve(upstreamFeed({ body: null })),
    );
    expect(response.status).toBe(200);
    expect(response.body).toBeNull();
    expect(response.headers.get("etag")).toBe('"abc"');
    const [, init] = fetchMock.mock.calls[0] ?? [];
    expect(init?.method).toBe("HEAD");
  });

  it("passes the API's 404 through", async () => {
    const { response } = await call("GET", "pending-agency", () =>
      Promise.resolve(
        Response.json(
          { code: "not_found", message: "agency not found" },
          { status: 404 },
        ),
      ),
    );
    expect(response.status).toBe(404);
    expect(await response.json()).toEqual({
      code: "not_found",
      message: "agency not found",
    });
  });

  it.each([
    "LA-Metro",
    "..",
    "a/b",
    "la metro",
    "-metro",
    "metro-",
    "a--b",
    "x".repeat(101),
    "%2e%2e",
  ])("rejects the slug %j without calling the API", async (slug) => {
    for (const method of ["GET", "HEAD"] as const) {
      const { response, fetchMock } = await call(method, slug, () =>
        Promise.resolve(upstreamFeed()),
      );
      expect(response.status).toBe(404);
      expect(response.headers.get("cache-control")).toBe("no-store");
      expect(fetchMock).not.toHaveBeenCalled();
      if (method === "HEAD") {
        expect(response.body).toBeNull();
      } else {
        expect(await response.text()).toBe("Not found\n");
      }
    }
  });

  it("does not follow redirects", async () => {
    const { response } = await call("GET", "la-metro", () =>
      Promise.resolve(
        new Response(null, {
          status: 302,
          headers: { location: "https://elsewhere.example/feed.xml" },
        }),
      ),
    );
    expect(response.status).toBe(502);
    expect(response.headers.get("location")).toBeNull();
  });

  it("treats an opaque redirect as unavailable", async () => {
    const opaque = upstreamFeed();
    Object.defineProperty(opaque, "type", { value: "opaqueredirect" });
    const { response } = await call("GET", "la-metro", () =>
      Promise.resolve(opaque),
    );
    expect(response.status).toBe(502);
  });

  it("answers 502 when the API cannot be reached", async () => {
    for (const method of ["GET", "HEAD"] as const) {
      const { response } = await call(method, "la-metro", () =>
        Promise.reject(new TypeError("fetch failed")),
      );
      expect(response.status).toBe(502);
      expect(response.headers.get("cache-control")).toBe("no-store");
    }
  });
});
