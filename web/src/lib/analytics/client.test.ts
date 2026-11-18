import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { WebVital } from "./client";

const posthog = vi.hoisted(() => ({
  init: vi.fn(),
  capture: vi.fn(),
}));

const POSTHOG_MODULE = "posthog-js/dist/module.slim.no-external";
const PROJECT = { key: "phc_test", host: "https://eu.i.posthog.com" };

async function loadAnalytics() {
  vi.resetModules();
  return import("./client");
}

async function settle() {
  await vi.dynamicImportSettled();
  await new Promise((resolve) => setTimeout(resolve, 0));
}

function vital(overrides: Partial<WebVital> = {}): WebVital {
  return {
    id: "v6-1",
    name: "LCP",
    value: 1234.5,
    delta: 1234.5,
    rating: "good",
    navigationType: "navigate",
    ...overrides,
  };
}

function captured() {
  return posthog.capture.mock.calls.map(([event, properties, options]) => ({
    event: event as string,
    properties: properties as Record<string, unknown>,
    options: options as Record<string, unknown> | undefined,
  }));
}

function sentVitals() {
  return captured()
    .filter(({ event }) => event === "$web_vitals")
    .map(({ properties }) => [properties.metric_name, properties.metric_value]);
}

function setVisibility(state: DocumentVisibilityState) {
  vi.spyOn(document, "visibilityState", "get").mockReturnValue(state);
}

function hidePage(order: "visibilitychange first" | "pagehide first") {
  if (order === "visibilitychange first") {
    setVisibility("hidden");
    document.dispatchEvent(new Event("visibilitychange"));
    window.dispatchEvent(new Event("pagehide"));
  } else {
    window.dispatchEvent(new Event("pagehide"));
    setVisibility("hidden");
    document.dispatchEvent(new Event("visibilitychange"));
  }
}

beforeEach(() => {
  posthog.init.mockReset();
  posthog.capture.mockReset();
  vi.doMock(POSTHOG_MODULE, () => ({ default: posthog }));
});

afterEach(() => {
  window.dispatchEvent(new Event("pageshow"));
  vi.doUnmock(POSTHOG_MODULE);
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("analytics before it is started", () => {
  it("loads nothing and sends nothing", async () => {
    const { trackPageview, reportWebVital } = await loadAnalytics();

    trackPageview("/", "");
    reportWebVital(vital());
    await settle();

    expect(posthog.init).not.toHaveBeenCalled();
    expect(posthog.capture).not.toHaveBeenCalled();
  });
});

describe("analytics", () => {
  it("starts PostHog once, anonymous, in memory only, behind the payload allow-list", async () => {
    const { startAnalytics } = await loadAnalytics();
    const { onlyAllowed } = await import("./payload");

    startAnalytics(PROJECT);
    startAnalytics(PROJECT);
    await settle();

    expect(posthog.init).toHaveBeenCalledOnce();
    expect(posthog.init).toHaveBeenCalledWith(
      "phc_test",
      expect.objectContaining({
        api_host: "https://eu.i.posthog.com",
        persistence: "memory",
        person_profiles: "identified_only",
        autocapture: false,
        capture_pageview: false,
        capture_pageleave: false,
        disable_session_recording: true,
        disable_external_dependency_loading: true,
        advanced_disable_flags: true,
        respect_dnt: true,
        before_send: onlyAllowed,
      }),
    );
  });

  it("sends one page view per path, without search params or hash", async () => {
    vi.stubGlobal("location", new URL("https://civic.example/"));
    const { startAnalytics, trackPageview } = await loadAnalytics();
    startAnalytics(PROJECT);

    trackPageview("/", "");
    trackPageview("/", "");
    trackPageview("/agencies", "page=2&email=jane%40example.com");
    trackPageview("/agencies", "page=3");
    trackPageview("/", "gclid=abc123");
    await settle();

    expect(captured()).toEqual([
      {
        event: "$pageview",
        properties: { $current_url: "https://civic.example/", $pathname: "/" },
        options: undefined,
      },
      {
        event: "$pageview",
        properties: {
          $current_url: "https://civic.example/agencies",
          $pathname: "/agencies",
        },
        options: undefined,
      },
      {
        event: "$pageview",
        properties: { $current_url: "https://civic.example/", $pathname: "/" },
        options: undefined,
      },
    ]);
  });

  it("sends FCP, LCP and TTFB once, as soon as they are reported", async () => {
    const { startAnalytics, reportWebVital } = await loadAnalytics();
    startAnalytics(PROJECT);

    reportWebVital(vital());
    reportWebVital(vital());
    reportWebVital(vital({ id: "v6-2", name: "TTFB", value: 80, delta: 80 }));
    await settle();

    expect(captured()).toEqual([
      {
        event: "$web_vitals",
        properties: {
          $web_vitals_LCP_value: 1234.5,
          $web_vitals_LCP_event: {
            id: "v6-1",
            name: "LCP",
            value: 1234.5,
            delta: 1234.5,
            rating: "good",
            navigationType: "navigate",
          },
          metric_name: "LCP",
          metric_value: 1234.5,
          metric_rating: "good",
        },
        options: { transport: "sendBeacon", send_instantly: true },
      },
      {
        event: "$web_vitals",
        properties: expect.objectContaining({
          metric_name: "TTFB",
          metric_value: 80,
        }) as Record<string, unknown>,
        options: { transport: "sendBeacon", send_instantly: true },
      },
    ]);
  });

  it.each(["visibilitychange first", "pagehide first"] as const)(
    "sends the final CLS and INP once when the page goes away (%s)",
    async (order) => {
      const { startAnalytics, reportWebVital } = await loadAnalytics();
      startAnalytics(PROJECT);
      const cls = vital({ id: "cls-1", name: "CLS", rating: "good" });
      const inp = vital({ id: "inp-1", name: "INP", rating: "good" });

      setVisibility("hidden");
      document.dispatchEvent(new Event("visibilitychange"));
      reportWebVital({ ...cls, value: 0.05 });
      reportWebVital({ ...inp, value: 40 });
      setVisibility("visible");
      document.dispatchEvent(new Event("visibilitychange"));
      await settle();
      expect(sentVitals()).toEqual([]);

      document.addEventListener(
        "visibilitychange",
        () => {
          reportWebVital({ ...cls, value: 0.3, rating: "poor" });
          reportWebVital({ ...inp, value: 250 });
        },
        { once: true, capture: true },
      );
      hidePage(order);
      reportWebVital({ ...cls, value: 0.4 });
      await settle();

      expect(sentVitals()).toEqual([
        ["CLS", 0.3],
        ["INP", 250],
      ]);
    },
  );

  it("files a web vital under the page it measured, without search params or hash", async () => {
    const { startAnalytics, reportWebVital } = await loadAnalytics();
    startAnalytics(PROJECT);

    reportWebVital(
      vital({
        navigationURL: "https://civic.example/agencies?page=2&gclid=x#top",
      }),
    );
    await settle();

    expect(captured()[0]?.properties).toMatchObject({
      $current_url: "https://civic.example/agencies",
      metric_name: "LCP",
    });
  });

  it("reports web vitals once per page load, not per client-side navigation", async () => {
    const { startAnalytics, reportWebVital } = await loadAnalytics();
    startAnalytics(PROJECT);

    reportWebVital(vital({ navigationType: "reload" }));
    reportWebVital(vital({ id: "v6-3", navigationType: "soft-navigation" }));
    await settle();

    expect(sentVitals()).toEqual([["LCP", 1234.5]]);
  });
});

describe("analytics for a visitor who opted out", () => {
  it.each([
    ["Global Privacy Control", { globalPrivacyControl: true }, {}],
    ["Do Not Track", { doNotTrack: "1" }, {}],
    ["Do Not Track in old Edge", { msDoNotTrack: "1" }, {}],
    ["Do Not Track in old Safari", {}, { doNotTrack: "1" }],
    ["Do Not Track in old Firefox", { doNotTrack: "yes" }, {}],
  ])("loads nothing under %s", async (_name, browser, page) => {
    vi.stubGlobal(
      "navigator",
      Object.assign(Object.create(navigator) as Navigator, browser),
    );
    for (const [name, value] of Object.entries(page)) {
      vi.stubGlobal(name, value);
    }
    const { startAnalytics, trackPageview, reportWebVital, visitorOptedOut } =
      await loadAnalytics();

    expect(visitorOptedOut()).toBe(true);
    startAnalytics(PROJECT);
    trackPageview("/", "");
    reportWebVital(vital());
    await settle();

    expect(posthog.init).not.toHaveBeenCalled();
    expect(posthog.capture).not.toHaveBeenCalled();
  });

  it("does not count Do Not Track turned off", async () => {
    vi.stubGlobal(
      "navigator",
      Object.assign(Object.create(navigator) as Navigator, {
        doNotTrack: "0",
        globalPrivacyControl: false,
      }),
    );
    const { visitorOptedOut } = await loadAnalytics();

    expect(visitorOptedOut()).toBe(false);
  });
});

describe("analytics when PostHog fails", () => {
  it("stays quiet when the PostHog script cannot load", async () => {
    vi.doMock(POSTHOG_MODULE, () => {
      throw new Error("blocked by an ad blocker");
    });
    const consoleError = vi.spyOn(console, "error");
    const { startAnalytics, trackPageview, reportWebVital } =
      await loadAnalytics();

    startAnalytics(PROJECT);
    trackPageview("/", "");
    reportWebVital(vital());
    await settle();

    expect(posthog.capture).not.toHaveBeenCalled();
    expect(consoleError).not.toHaveBeenCalled();
  });

  it("stays quiet when PostHog fails to start", async () => {
    posthog.init.mockImplementation(() => {
      throw new Error("init failed");
    });
    const { startAnalytics, trackPageview } = await loadAnalytics();

    startAnalytics(PROJECT);
    trackPageview("/", "");
    await settle();

    expect(posthog.capture).not.toHaveBeenCalled();
  });

  it("keeps sending when one capture throws", async () => {
    posthog.capture.mockImplementationOnce(() => {
      throw new Error("capture failed");
    });
    const { startAnalytics, trackPageview } = await loadAnalytics();

    startAnalytics(PROJECT);
    trackPageview("/", "");
    trackPageview("/agencies", "");
    await settle();

    expect(posthog.capture).toHaveBeenCalledTimes(2);
  });
});
