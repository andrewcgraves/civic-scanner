import "server-only";

import { OpenFeature, type Provider } from "@openfeature/server-sdk";
import { PostHogServerProvider } from "@posthog/openfeature-node-provider";
import { PostHog } from "posthog-node";

import { readFlagSource, type FlagSource } from "./config";
import { memoryServerProvider } from "./memory";

const ANONYMOUS_DISTINCT_ID = "anonymous";

let ready: Promise<void> | undefined;

function createProvider(source: FlagSource): Provider {
  if (source.kind === "memory") {
    return memoryServerProvider(source.on);
  }
  return new PostHogServerProvider(
    new PostHog(source.key, { host: source.host }),
    { defaultDistinctId: ANONYMOUS_DISTINCT_ID, sendFeatureFlagEvents: false },
  );
}

export async function isFlagEnabled(flag: string): Promise<boolean> {
  ready ??= OpenFeature.setProviderAndWait(
    createProvider(readFlagSource()),
  ).catch((error: unknown) => {
    console.error("Feature flag provider failed to start", error);
  });
  await ready;
  return OpenFeature.getClient().getBooleanValue(flag, false);
}
