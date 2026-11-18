import { OpenFeature as ServerFeature } from "@openfeature/server-sdk";
import { OpenFeature as WebFeature } from "@openfeature/web-sdk";
import { afterEach, describe, expect, it } from "vitest";

import { memoryServerProvider, memoryWebProvider } from "./memory";

describe("in-memory providers", () => {
  afterEach(async () => {
    await ServerFeature.clearProviders();
    await WebFeature.clearProviders();
  });

  it("serve only boolean flags on the server", async () => {
    await ServerFeature.setProviderAndWait(
      "memory-test",
      memoryServerProvider("all"),
    );
    const client = ServerFeature.getClient("memory-test");

    expect(await client.getBooleanValue("theme", false)).toBe(true);
    expect(await client.getStringValue("theme", "light")).toBe("light");
    expect(await client.getNumberValue("theme", 3)).toBe(3);
    expect(await client.getObjectValue("theme", { a: 1 })).toEqual({ a: 1 });
  });

  it("serve only boolean flags in the browser", async () => {
    await WebFeature.setProviderAndWait(
      "memory-test",
      memoryWebProvider("all"),
    );
    const client = WebFeature.getClient("memory-test");

    expect(client.getBooleanValue("theme", false)).toBe(true);
    expect(client.getStringValue("theme", "light")).toBe("light");
    expect(client.getNumberValue("theme", 3)).toBe(3);
    expect(client.getObjectValue("theme", { a: 1 })).toEqual({ a: 1 });
  });
});
