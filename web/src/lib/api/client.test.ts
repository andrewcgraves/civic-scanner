import { afterEach, describe, expect, it, vi } from "vitest";

async function loadBaseUrl(configured: string | undefined) {
  vi.stubEnv("NEXT_PUBLIC_API_BASE_URL", configured);
  vi.resetModules();
  const { apiBaseUrl } = await import("./client");
  return apiBaseUrl;
}

describe("apiBaseUrl", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("defaults to the local API when unset", async () => {
    expect(await loadBaseUrl(undefined)).toBe("http://localhost:8080/v1");
  });

  it("defaults to the local API when blank", async () => {
    expect(await loadBaseUrl("   ")).toBe("http://localhost:8080/v1");
  });

  it("uses the configured URL, trimmed", async () => {
    expect(await loadBaseUrl(" https://api.example.com/v1 ")).toBe(
      "https://api.example.com/v1",
    );
  });
});
