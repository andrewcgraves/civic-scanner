"use client";

import {
  OpenFeature,
  OpenFeatureProvider,
  useBooleanFlagValue,
  type Provider,
} from "@openfeature/react-sdk";
import { PostHogWebProvider } from "@posthog/openfeature-web-provider";
import posthog from "posthog-js";
import type { ReactNode } from "react";

import { readFlagSource, type FlagSource } from "./config";
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

function createProvider(source: FlagSource): Provider {
  if (source.kind === "memory") {
    return memoryWebProvider(source.on);
  }
  if (typeof window === "undefined") {
    return memoryWebProvider([]);
  }
  posthog.init(source.key, { api_host: source.host, ...FLAGS_ONLY });
  return new PostHogWebProvider(posthog, { sendFeatureFlagEvents: false });
}

OpenFeature.setProvider(createProvider(readFlagSource()));

export function FlagsProvider({ children }: { children: ReactNode }) {
  return <OpenFeatureProvider>{children}</OpenFeatureProvider>;
}

export function useFlag(flag: string): boolean {
  return useBooleanFlagValue(flag, false);
}
