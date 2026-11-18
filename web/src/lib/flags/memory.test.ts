import { OpenFeature } from "@openfeature/server-sdk";
import { afterEach, describe, expect, it } from "vitest";

import type { FlagsOn } from "./config";
import { memoryProvider } from "./memory";

async function flagsClient(on: FlagsOn) {
  await OpenFeature.setProviderAndWait("memory-test", memoryProvider(on));
  return OpenFeature.getClient("memory-test");
}

describe("memoryProvider", () => {
  afterEach(async () => {
    await OpenFeature.clearProviders();
  });

  it("turns on only the listed flags", async () => {
    const client = await flagsClient(["new-search", "meeting-alerts"]);

    expect(await client.getBooleanValue("new-search", false)).toBe(true);
    expect(await client.getBooleanValue("meeting-alerts", false)).toBe(true);
    expect(await client.getBooleanValue("new-search-v2", false)).toBe(false);
  });

  it("turns on every flag when all are on", async () => {
    const client = await flagsClient("all");

    expect(await client.getBooleanValue("anything-at-all", false)).toBe(true);
  });

  it("serves only boolean flags", async () => {
    const client = await flagsClient("all");

    expect(await client.getStringValue("theme", "light")).toBe("light");
    expect(await client.getNumberValue("theme", 3)).toBe(3);
    expect(await client.getObjectValue("theme", { a: 1 })).toEqual({ a: 1 });
  });
});
