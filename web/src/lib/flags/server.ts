import "server-only";

import { connection } from "next/server";
import { PostHog } from "posthog-node";
import { cache } from "react";

import { isOn, readFlagSource, type FlagSource, type FlagsOn } from "./config";

const ANONYMOUS_DISTINCT_ID = "anonymous";
const POSTHOG_TIMEOUT_MS = 500;
const POSTHOG_CACHE_TTL_MS = 30_000;
const NONE_ON: FlagsOn = [];

type PostHogSource = Extract<FlagSource, { kind: "posthog" }>;

const source = readFlagSource();

let posthog: PostHog | undefined;

let cached: { on: Promise<FlagsOn>; expiresAt: number } | undefined;

function reportFailure(error: unknown) {
  console.error("Feature flags could not be evaluated", error);
}

function createPostHog(source: PostHogSource): PostHog {
  const client = new PostHog(source.key, {
    host: source.host,
    featureFlagsRequestTimeoutMs: POSTHOG_TIMEOUT_MS,
    featureFlagsRequestMaxRetries: 0,
  });
  client.on("error", reportFailure);
  return client;
}

async function askPostHog(
  client: PostHog,
  distinctId: string,
): Promise<FlagsOn> {
  try {
    const flags = await client.evaluateFlags(distinctId);
    return flags.keys.filter((flag) =>
      flags.isEnabled(flag, { sendFeatureFlagEvents: false }),
    );
  } catch (error) {
    reportFailure(error);
    return NONE_ON;
  }
}

function askPostHogCached(source: PostHogSource): Promise<FlagsOn> {
  const now = Date.now();
  if (cached === undefined || cached.expiresAt <= now) {
    posthog ??= createPostHog(source);
    cached = {
      on: askPostHog(posthog, ANONYMOUS_DISTINCT_ID),
      expiresAt: now + POSTHOG_CACHE_TTL_MS,
    };
  }
  return cached.on;
}

export const getFlagsOn = cache(async (): Promise<FlagsOn> => {
  if (source.kind === "memory") {
    return source.on;
  }
  await connection();
  return askPostHogCached(source);
});

export async function isFlagEnabled(flag: string): Promise<boolean> {
  return isOn(await getFlagsOn(), flag);
}
