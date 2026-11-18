import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("next/server", () => ({ connection: () => Promise.resolve() }));

async function renderLayoutWithFlags(env: Record<string, string | undefined>) {
  for (const [name, value] of Object.entries(env)) {
    vi.stubEnv(name, value);
  }
  vi.resetModules();
  const { default: Layout } = await import("./layout");
  const { useFlag } = await import("@/lib/flags/client");

  function Gated() {
    return <p>{useFlag("test-only-flag") ? "on" : "off"}</p>;
  }

  return new DOMParser().parseFromString(
    renderToStaticMarkup(await Layout({ children: <Gated /> })),
    "text/html",
  );
}

describe("RootLayout", () => {
  afterEach(async () => {
    const { OpenFeature } = await import("@openfeature/server-sdk");
    await OpenFeature.clearProviders();
    vi.unstubAllEnvs();
    vi.unstubAllGlobals();
  });

  it("wraps the page in an English document", async () => {
    const { default: RootLayout } = await import("./layout");
    const doc = new DOMParser().parseFromString(
      renderToStaticMarkup(await RootLayout({ children: <p>page content</p> })),
      "text/html",
    );

    expect(doc.documentElement.getAttribute("lang")).toBe("en");
    expect(doc.body.innerHTML).toBe("<p>page content</p>");
  });

  it("sets the site title", async () => {
    const { metadata } = await import("./layout");

    expect(metadata.title).toBe("Civic Scanner");
  });

  describe("analytics", () => {
    async function renderWithAnalyticsMarker(
      env: Record<string, string | undefined>,
    ) {
      vi.doMock("@/lib/analytics/analytics", () => ({
        Analytics: ({
          project,
        }: {
          project: { key: string; host: string };
        }) => <i data-key={project.key} data-host={project.host} />,
      }));
      try {
        return await renderLayoutWithFlags(env);
      } finally {
        vi.doUnmock("@/lib/analytics/analytics");
      }
    }

    it("is left out when there is no PostHog key", async () => {
      const doc = await renderWithAnalyticsMarker({
        NEXT_PUBLIC_POSTHOG_KEY: undefined,
      });

      expect(doc.querySelector("i")).toBeNull();
    });

    it("gets the PostHog project when there is a key", async () => {
      vi.stubGlobal(
        "fetch",
        vi.fn(() => Promise.resolve(Response.json({ flags: {} }))),
      );

      const doc = await renderWithAnalyticsMarker({
        NEXT_PUBLIC_POSTHOG_KEY: "phc_test",
        NEXT_PUBLIC_POSTHOG_HOST: "https://eu.i.posthog.com",
      });

      const marker = doc.querySelector("i");
      expect(marker?.dataset.key).toBe("phc_test");
      expect(marker?.dataset.host).toBe("https://eu.i.posthog.com");
    });
  });

  it("gives client components the in-memory flags", async () => {
    const doc = await renderLayoutWithFlags({
      NEXT_PUBLIC_POSTHOG_KEY: undefined,
      NEXT_PUBLIC_FEATURE_FLAGS: "test-only-flag",
    });

    expect(doc.body.innerHTML).toBe("<p>on</p>");
  });

  it("gives client components the flags the server got from PostHog", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          Response.json({
            flags: {
              "test-only-flag": {
                key: "test-only-flag",
                enabled: true,
                variant: null,
                reason: { code: "condition_match" },
                metadata: { id: 1, version: 1 },
              },
            },
          }),
        ),
      ),
    );

    const doc = await renderLayoutWithFlags({
      NEXT_PUBLIC_POSTHOG_KEY: "phc_test",
      NEXT_PUBLIC_FEATURE_FLAGS: undefined,
    });

    expect(doc.body.innerHTML).toBe("<p>on</p>");
  });
});
