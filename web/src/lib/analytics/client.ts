import type {
  CaptureOptions,
  PostHog,
  PostHogConfig,
} from "posthog-js/dist/module.slim.no-external";

import type { PostHogProject } from "@/lib/posthog/config";

import { onlyAllowed, pageUrl, type WEB_VITALS } from "./payload";

export interface WebVital {
  id: string;
  name: (typeof WEB_VITALS)[number];
  value: number;
  delta: number;
  rating: "good" | "needs-improvement" | "poor";
  navigationType: string;
  navigationURL?: string;
}

type Capture = Pick<PostHog, "capture">;

const SOFT_NAVIGATION = "soft-navigation";
const FINAL_WHEN_REPORTED: readonly WebVital["name"][] = ["FCP", "LCP", "TTFB"];

const privacyConfig = (host: string): Partial<PostHogConfig> => ({
  api_host: host,
  defaults: "2026-08-30",
  persistence: "memory",
  person_profiles: "identified_only",
  autocapture: false,
  capture_pageview: false,
  capture_pageleave: false,
  capture_performance: false,
  capture_heatmaps: false,
  capture_dead_clicks: false,
  capture_exceptions: false,
  disable_session_recording: true,
  disable_surveys: true,
  disable_product_tours: true,
  disable_conversations: true,
  disable_web_experiments: true,
  disable_external_dependency_loading: true,
  advanced_disable_flags: true,
  respect_dnt: true,
  save_referrer: false,
  save_campaign_params: false,
  internal_or_test_user_hostname: null,
  before_send: onlyAllowed,
});

let client: Promise<Capture | undefined> | undefined;
let lastPageview: string | undefined;
let unloading = false;
const pendingVitals = new Map<string, WebVital>();
const sentVitals = new Set<string>();

export function visitorOptedOut(): boolean {
  const browser = navigator as Navigator & {
    globalPrivacyControl?: boolean;
    msDoNotTrack?: string;
  };
  const page = window as Window & { doNotTrack?: string };
  return (
    browser.globalPrivacyControl === true ||
    [browser.doNotTrack, browser.msDoNotTrack, page.doNotTrack].some(
      (value) => value === "1" || value === "yes",
    )
  );
}

async function load(project: PostHogProject): Promise<Capture | undefined> {
  try {
    const { default: posthog } =
      await import("posthog-js/dist/module.slim.no-external");
    posthog.init(project.key, privacyConfig(project.host));
    return posthog;
  } catch {
    return undefined;
  }
}

function send(
  event: string,
  properties: Record<string, unknown>,
  options?: CaptureOptions,
): void {
  void client?.then((posthog) => {
    try {
      posthog?.capture(event, properties, options);
    } catch {
      return;
    }
  });
}

function sendVital(metric: WebVital): void {
  sentVitals.add(metric.id);
  const { id, name, value, delta, rating, navigationType } = metric;
  const page =
    metric.navigationURL === undefined
      ? {}
      : { $current_url: pageUrl(metric.navigationURL).href };
  send(
    "$web_vitals",
    {
      ...page,
      [`$web_vitals_${name}_value`]: value,
      [`$web_vitals_${name}_event`]: {
        id,
        name,
        value,
        delta,
        rating,
        navigationType,
      },
      metric_name: name,
      metric_value: value,
      metric_rating: rating,
    },
    { transport: "sendBeacon", send_instantly: true },
  );
}

function flushVitals(): void {
  for (const metric of pendingVitals.values()) {
    sendVital(metric);
  }
  pendingVitals.clear();
}

function watchPageHide(): void {
  addEventListener("pagehide", () => {
    unloading = true;
    if (document.visibilityState === "hidden") {
      flushVitals();
    }
  });
  addEventListener("pageshow", () => {
    unloading = false;
  });
  document.addEventListener("visibilitychange", () => {
    if (unloading && document.visibilityState === "hidden") {
      flushVitals();
    }
  });
}

export function startAnalytics(project: PostHogProject): void {
  if (client !== undefined) {
    return;
  }
  if (visitorOptedOut()) {
    client = Promise.resolve(undefined);
    return;
  }
  watchPageHide();
  client = load(project);
}

export function trackPageview(pathname: string, search: string): void {
  const url = pageUrl(
    new URL(`${pathname}?${search}`, window.location.origin).href,
  );
  if (url.href === lastPageview) {
    return;
  }
  lastPageview = url.href;
  send("$pageview", { $current_url: url.href, $pathname: url.pathname });
}

export function reportWebVital(metric: WebVital): void {
  if (metric.navigationType === SOFT_NAVIGATION || sentVitals.has(metric.id)) {
    return;
  }
  if (FINAL_WHEN_REPORTED.includes(metric.name)) {
    sendVital(metric);
    return;
  }
  pendingVitals.set(metric.id, metric);
}
