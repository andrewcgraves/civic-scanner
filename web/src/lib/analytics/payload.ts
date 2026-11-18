import type { CaptureResult } from "posthog-js/dist/module.slim.no-external";

export const ALLOWED_SEARCH_PARAMS: readonly string[] = [];

export const WEB_VITALS = ["CLS", "FCP", "INP", "LCP", "TTFB"] as const;

const NEEDED_BY_POSTHOG = [
  "token",
  "distinct_id",
  "$lib",
  "$lib_version",
  "$process_person_profile",
];

const ALLOWED_PROPERTIES: Readonly<Record<string, readonly string[]>> = {
  $pageview: NEEDED_BY_POSTHOG,
  $web_vitals: [
    ...NEEDED_BY_POSTHOG,
    "metric_name",
    "metric_value",
    "metric_rating",
    ...WEB_VITALS.flatMap((name) => [
      `$web_vitals_${name}_value`,
      `$web_vitals_${name}_event`,
    ]),
  ],
};

export function pageUrl(href: string): URL {
  const url = new URL(href);
  const kept = [...url.searchParams].filter(([name]) =>
    ALLOWED_SEARCH_PARAMS.includes(name),
  );
  url.search = new URLSearchParams(kept).toString();
  url.hash = "";
  return url;
}

function pageOf(currentUrl: unknown) {
  if (typeof currentUrl !== "string") {
    return {};
  }
  const url = pageUrl(currentUrl);
  return { $current_url: url.href, $pathname: url.pathname };
}

export function onlyAllowed(
  captured: CaptureResult | null,
): CaptureResult | null {
  const allowed =
    captured === null ? undefined : ALLOWED_PROPERTIES[captured.event];
  if (captured === null || allowed === undefined) {
    return null;
  }
  const { uuid, event, timestamp, properties } = captured;
  return {
    uuid,
    event,
    timestamp,
    properties: {
      ...Object.fromEntries(
        allowed
          .filter((name) => name in properties)
          .map((name) => [name, properties[name]]),
      ),
      ...pageOf(properties.$current_url),
      $geoip_disable: true,
    },
  };
}
