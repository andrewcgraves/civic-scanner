"use client";

import {
  OpenFeature,
  OpenFeatureProvider,
  useBooleanFlagValue,
} from "@openfeature/react-sdk";
import type { ReactNode } from "react";

import { readFlagSource } from "./config";
import { memoryWebProvider } from "./memory";

const FLAGS_ONLY = {
  persistence: "memory",
  disable_external_dependency_loading: true,
  advanced_disable_feature_flags_on_first_load: true,
  autocapture: false,
  capture_pageview: false,
  capture_pageleave: false,
  capture_performance: false,
  capture_heatmaps: false,
  capture_dead_clicks: false,
  capture_exceptions: false,
  rageclick: false,
  disable_session_recording: true,
  disable_surveys: true,
  disable_product_tours: true,
  disable_conversations: true,
  disable_web_experiments: true,
} as const;

async function startPostHog(key: string, host: string): Promise<void> {
  const [{ default: posthog }, { PostHogWebProvider }] = await Promise.all([
    import("posthog-js"),
    import("@posthog/openfeature-web-provider"),
  ]);
  posthog.init(key, { api_host: host, ...FLAGS_ONLY });
  OpenFeature.setProvider(
    new PostHogWebProvider(posthog, { sendFeatureFlagEvents: false }),
  );
}

const source = readFlagSource();
OpenFeature.setProvider(
  memoryWebProvider(source.kind === "memory" ? source.on : []),
);
if (source.kind === "posthog" && typeof window !== "undefined") {
  startPostHog(source.key, source.host).catch((error: unknown) => {
    console.error("Feature flags could not start PostHog", error);
  });
}

export function FlagsProvider({ children }: { children: ReactNode }) {
  return <OpenFeatureProvider>{children}</OpenFeatureProvider>;
}

export function useFlag(flag: string): boolean {
  return useBooleanFlagValue(flag, false);
}
