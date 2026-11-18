import type {
  CaptureOptions,
  PostHog,
  PostHogConfig,
} from "posthog-js/dist/module.slim";

import type { PostHogProject } from "@/lib/posthog/config";

export interface WebVital {
  id: string;
  name: "CLS" | "FCP" | "INP" | "LCP" | "TTFB";
  value: number;
  delta: number;
  rating: "good" | "needs-improvement" | "poor";
  navigationType: string;
  navigationURL?: string;
}

type Capture = Pick<PostHog, "capture">;

const SOFT_NAVIGATION = "soft-navigation";

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
  mask_personal_data_properties: true,
  disable_capture_url_hashes: true,
  save_referrer: false,
  save_campaign_params: false,
  property_denylist: ["$referrer", "$initial_referrer"],
  internal_or_test_user_hostname: null,
});

let client: Promise<Capture | undefined> | undefined;
let lastPageview: string | undefined;
const reportedVitals = new Set<string>();

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
    const { default: posthog } = await import("posthog-js/dist/module.slim");
    posthog.init(project.key, privacyConfig(project.host));
    return posthog;
  } catch {
    return undefined;
  }
}

export function startAnalytics(project: PostHogProject): void {
  client ??= visitorOptedOut() ? Promise.resolve(undefined) : load(project);
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

export function trackPageview(pathname: string, search: string): void {
  const url = new URL(
    search === "" ? pathname : `${pathname}?${search}`,
    window.location.origin,
  ).href;
  if (url === lastPageview) {
    return;
  }
  lastPageview = url;
  send("$pageview", { $current_url: url, $pathname: pathname });
}

function pageOf(navigationURL: string | undefined) {
  if (navigationURL === undefined) {
    return {};
  }
  const url = new URL(navigationURL);
  url.hash = "";
  return { $current_url: url.href, $pathname: url.pathname };
}

export function reportWebVital(metric: WebVital): void {
  if (
    metric.navigationType === SOFT_NAVIGATION ||
    reportedVitals.has(metric.id)
  ) {
    return;
  }
  reportedVitals.add(metric.id);
  const { id, name, value, delta, rating, navigationType } = metric;
  send(
    "$web_vitals",
    {
      ...pageOf(metric.navigationURL),
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
