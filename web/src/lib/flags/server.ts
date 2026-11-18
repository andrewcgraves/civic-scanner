import "server-only";

import { OpenFeature, type Provider } from "@openfeature/server-sdk";
import { connection } from "next/server";
import { cache } from "react";

import { readFlagSource, type FlagSource } from "./config";
import { memoryProvider } from "./memory";
import { PostHogProvider } from "./posthog";
import { FLAG_KEYS, type FlagKey } from "./registry";

const ANONYMOUS_DISTINCT_ID = "anonymous";

const source = readFlagSource();

let registered: Promise<void> | undefined;

function createProvider(source: FlagSource): Provider {
  if (source.kind === "memory") {
    return memoryProvider(source.on);
  }
  return new PostHogProvider({
    key: source.key,
    host: source.host,
    distinctId: ANONYMOUS_DISTINCT_ID,
  });
}

function registerProvider(): Promise<void> {
  registered ??= OpenFeature.setProviderAndWait(createProvider(source)).catch(
    (error: unknown) => {
      console.error("Feature flag provider failed to start", error);
    },
  );
  return registered;
}

export const getFlagsOn = cache(async (): Promise<readonly FlagKey[]> => {
  if (source.kind === "posthog") {
    await connection();
  }
  await registerProvider();
  const client = OpenFeature.getClient();
  const answers = await Promise.all(
    FLAG_KEYS.map((flag) => client.getBooleanValue(flag, false)),
  );
  return FLAG_KEYS.filter((_flag, index) => answers[index]);
});

export async function isFlagEnabled(flag: FlagKey): Promise<boolean> {
  return (await getFlagsOn()).includes(flag);
}
