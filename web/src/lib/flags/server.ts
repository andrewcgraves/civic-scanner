import "server-only";

import { OpenFeature, type Provider } from "@openfeature/server-sdk";
import { PostHogServerProvider } from "@posthog/openfeature-node-provider";
import { connection } from "next/server";
import { PostHog } from "posthog-node";

import { readFlagSource, type FlagSource } from "./config";
import { memoryServerProvider } from "./memory";

const ANONYMOUS_DISTINCT_ID = "anonymous";
const POSTHOG_TIMEOUT_MS = 500;
const POSTHOG_CACHE_TTL_MS = 30_000;

const source = readFlagSource();

let ready: Promise<void> | undefined;

const cache = new Map<string, { value: Promise<boolean>; expiresAt: number }>();

function createProvider(source: FlagSource): Provider {
  if (source.kind === "memory") {
    return memoryServerProvider(source.on);
  }
  return new PostHogServerProvider(
    new PostHog(source.key, {
      host: source.host,
      featureFlagsRequestTimeoutMs: POSTHOG_TIMEOUT_MS,
      featureFlagsRequestMaxRetries: 0,
    }),
    { defaultDistinctId: ANONYMOUS_DISTINCT_ID, sendFeatureFlagEvents: false },
  );
}

async function evaluate(flag: string): Promise<boolean> {
  ready ??= OpenFeature.setProviderAndWait(createProvider(source)).catch(
    (error: unknown) => {
      console.error("Feature flag provider failed to start", error);
    },
  );
  await ready;
  return OpenFeature.getClient().getBooleanValue(flag, false);
}

function evaluateCached(flag: string): Promise<boolean> {
  const now = Date.now();
  const hit = cache.get(flag);
  if (hit !== undefined && hit.expiresAt > now) {
    return hit.value;
  }
  const value = evaluate(flag);
  cache.set(flag, { value, expiresAt: now + POSTHOG_CACHE_TTL_MS });
  return value;
}

export async function isFlagEnabled(flag: string): Promise<boolean> {
  if (source.kind === "memory") {
    return evaluate(flag);
  }
  await connection();
  return evaluateCached(flag);
}
