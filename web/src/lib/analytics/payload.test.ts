import type { CaptureResult } from "posthog-js/dist/module.slim.no-external";
import { describe, expect, it } from "vitest";

import { onlyAllowed, pageUrl } from "./payload";

function capturedEvent(
  event: string,
  properties: Record<string, unknown>,
): CaptureResult {
  return {
    uuid: "0190-uuid",
    event,
    properties,
    $set: { email: "jane@example.com" },
    $set_once: { $initial_referrer: "https://search.example/?q=jane" },
    timestamp: new Date("2026-11-18T12:00:00Z"),
  };
}

describe("pageUrl", () => {
  it("keeps the origin and path and drops search params and hash", () => {
    expect(
      pageUrl("https://civic.example/agencies?page=2&gclid=abc#top").href,
    ).toBe("https://civic.example/agencies");
  });
});

describe("onlyAllowed", () => {
  it("rebuilds a page view from the allowed properties only", () => {
    const result = onlyAllowed(
      capturedEvent("$pageview", {
        token: "phc_test",
        distinct_id: "anon-1",
        $lib: "web",
        $lib_version: "1.445.6",
        $process_person_profile: false,
        $current_url: "https://civic.example/agencies?email=jane%40example.com",
        $pathname: "/agencies",
        $session_id: "session-1",
        $referrer: "https://search.example/?q=jane",
        $session_entry_referrer: "https://search.example/?q=jane",
        $session_entry_utm_source: "news",
        $session_entry_gclid: "abc123",
        $browser: "Chrome",
        $screen_width: 1512,
      }),
    );

    expect(result).toEqual({
      uuid: "0190-uuid",
      event: "$pageview",
      timestamp: new Date("2026-11-18T12:00:00Z"),
      properties: {
        token: "phc_test",
        distinct_id: "anon-1",
        $lib: "web",
        $lib_version: "1.445.6",
        $process_person_profile: false,
        $current_url: "https://civic.example/agencies",
        $pathname: "/agencies",
        $geoip_disable: true,
      },
    });
  });

  it("keeps the web vital fields of a web vitals event", () => {
    const result = onlyAllowed(
      capturedEvent("$web_vitals", {
        distinct_id: "anon-1",
        $current_url: "https://civic.example/",
        $web_vitals_CLS_value: 0.3,
        $web_vitals_CLS_event: { name: "CLS", value: 0.3 },
        metric_name: "CLS",
        metric_value: 0.3,
        metric_rating: "poor",
        $viewport_width: 360,
      }),
    );

    expect(result?.properties).toEqual({
      distinct_id: "anon-1",
      $current_url: "https://civic.example/",
      $pathname: "/",
      $web_vitals_CLS_value: 0.3,
      $web_vitals_CLS_event: { name: "CLS", value: 0.3 },
      metric_name: "CLS",
      metric_value: 0.3,
      metric_rating: "poor",
      $geoip_disable: true,
    });
  });

  it("leaves the page out when PostHog sent none", () => {
    const result = onlyAllowed(
      capturedEvent("$pageview", { distinct_id: "anon-1" }),
    );

    expect(result?.properties).toEqual({
      distinct_id: "anon-1",
      $geoip_disable: true,
    });
  });

  it("drops every other event", () => {
    expect(onlyAllowed(capturedEvent("$identify", {}))).toBeNull();
    expect(onlyAllowed(capturedEvent("$$client_ingestion_warning", {}))).toBe(
      null,
    );
    expect(onlyAllowed(null)).toBeNull();
  });
});
