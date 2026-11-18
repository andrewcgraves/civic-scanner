import { gunzipSync } from "node:zlib";

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const HOST = "https://ph.civic.test";
const LANDING =
  "/agencies?q=jane.doe%40mail.com&gclid=abc123&utm_source=news#section";
const REFERRER = "https://search.example/?q=my+name";
const BASE_PROPERTIES = [
  "token",
  "distinct_id",
  "$lib",
  "$lib_version",
  "$process_person_profile",
  "$current_url",
  "$pathname",
  "$geoip_disable",
];

interface Request {
  url: string;
  body: unknown;
}

interface SentEvent {
  event: string;
  properties: Record<string, unknown>;
}

const requests: Request[] = [];

class RecordingXMLHttpRequest {
  url = "";
  readyState = 0;
  status = 0;
  responseText = "{}";
  onreadystatechange: (() => void) | null = null;
  open(_method: string, url: string) {
    this.url = url;
  }
  setRequestHeader() {
    return undefined;
  }
  send(body: unknown) {
    requests.push({ url: this.url, body });
    this.readyState = 4;
    this.status = 200;
    this.onreadystatechange?.();
  }
}

async function text(body: unknown): Promise<string> {
  if (typeof body === "string") {
    return body;
  }
  const bytes =
    body instanceof Blob
      ? Buffer.from(await body.arrayBuffer())
      : Buffer.from(body as Uint8Array);
  try {
    return gunzipSync(bytes).toString("utf8");
  } catch {
    return bytes.toString("utf8");
  }
}

async function sentEvents(): Promise<SentEvent[]> {
  const events: SentEvent[] = [];
  for (const request of requests) {
    const parsed = JSON.parse(await text(request.body)) as
      { batch?: SentEvent[] } | SentEvent | SentEvent[];
    if (Array.isArray(parsed)) {
      events.push(...parsed);
    } else if ("batch" in parsed && parsed.batch !== undefined) {
      events.push(...parsed.batch);
    } else {
      events.push(parsed as SentEvent);
    }
  }
  return events;
}

beforeEach(() => {
  requests.length = 0;
  window.history.replaceState(null, "", LANDING);
  Object.defineProperty(document, "referrer", {
    value: REFERRER,
    configurable: true,
  });
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string, init?: RequestInit) => {
      requests.push({ url, body: init?.body });
      return Promise.resolve(Response.json({ status: 1 }));
    }),
  );
  vi.stubGlobal("XMLHttpRequest", RecordingXMLHttpRequest);
  Object.defineProperty(navigator, "sendBeacon", {
    value: (url: string, body: unknown) => {
      requests.push({ url, body });
      return true;
    },
    configurable: true,
  });
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("analytics with the real PostHog client", () => {
  it("sends only the allowed properties to /e/ and stores nothing", async () => {
    const setItem = vi.spyOn(Storage.prototype, "setItem");
    const setCookie = vi.spyOn(Document.prototype, "cookie", "set");
    const createElement = vi.spyOn(document, "createElement");
    const { startAnalytics, trackPageview, reportWebVital } =
      await import("./client");

    startAnalytics({ key: "phc_test", host: HOST });
    trackPageview("/agencies", window.location.search.slice(1));
    reportWebVital({
      id: "lcp-1",
      name: "LCP",
      value: 1200,
      delta: 1200,
      rating: "good",
      navigationType: "navigate",
      navigationURL: window.location.href,
    });
    reportWebVital({
      id: "cls-1",
      name: "CLS",
      value: 0.02,
      delta: 0.02,
      rating: "good",
      navigationType: "navigate",
    });
    await vi.dynamicImportSettled();
    window.dispatchEvent(new Event("pagehide"));
    vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    document.dispatchEvent(new Event("visibilitychange"));

    await vi.waitFor(
      async () => {
        expect((await sentEvents()).map(({ event }) => event).sort()).toEqual([
          "$pageview",
          "$web_vitals",
          "$web_vitals",
        ]);
      },
      { timeout: 8_000, interval: 100 },
    );

    const events = await sentEvents();
    expect(new Set(requests.map(({ url }) => url))).toEqual(
      new Set([`${HOST}/e/`]),
    );
    for (const { event, properties } of events) {
      const vitalName = properties.metric_name as string;
      expect(Object.keys(properties).sort()).toEqual(
        [
          ...BASE_PROPERTIES,
          ...(event === "$web_vitals"
            ? [
                "metric_name",
                "metric_value",
                "metric_rating",
                `$web_vitals_${vitalName}_value`,
                `$web_vitals_${vitalName}_event`,
              ]
            : []),
        ].sort(),
      );
      expect(properties).toMatchObject({
        $current_url: "http://localhost:3000/agencies",
        $pathname: "/agencies",
        $process_person_profile: false,
        $geoip_disable: true,
      });
    }
    const payloads = (
      await Promise.all(requests.map(({ body }) => text(body)))
    ).join("\n");
    for (const leak of [
      "?",
      "#section",
      "jane",
      "gclid",
      "utm",
      "search.example",
      "referr",
      "$session",
    ]) {
      expect(payloads).not.toContain(leak);
    }

    expect(setCookie).not.toHaveBeenCalled();
    expect(document.cookie).toBe("");
    expect(setItem.mock.calls).toEqual([["__mplssupport__", '"xyz"']]);
    expect(localStorage).toHaveLength(0);
    expect(sessionStorage).toHaveLength(0);
    expect(
      createElement.mock.calls.filter(([tag]) => tag === "script"),
    ).toEqual([]);
  }, 10_000);
});
