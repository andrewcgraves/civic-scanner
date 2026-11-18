import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import RootLayout, { metadata } from "./layout";

describe("RootLayout", () => {
  it("wraps the page in an English document", () => {
    const doc = new DOMParser().parseFromString(
      renderToStaticMarkup(
        <RootLayout>
          <p>page content</p>
        </RootLayout>,
      ),
      "text/html",
    );

    expect(doc.documentElement.getAttribute("lang")).toBe("en");
    expect(doc.body.innerHTML).toBe("<p>page content</p>");
  });

  it("sets the site title", () => {
    expect(metadata.title).toBe("Civic Scanner");
  });

  it("gives client components access to feature flags", async () => {
    vi.stubEnv("NEXT_PUBLIC_POSTHOG_KEY", undefined);
    vi.stubEnv("NEXT_PUBLIC_FEATURE_FLAGS", "new-search");
    vi.resetModules();
    const { default: Layout } = await import("./layout");
    const { useFlag } = await import("@/lib/flags/client");

    function Gated() {
      return <p>{useFlag("new-search") ? "on" : "off"}</p>;
    }

    const doc = new DOMParser().parseFromString(
      renderToStaticMarkup(
        <Layout>
          <Gated />
        </Layout>,
      ),
      "text/html",
    );
    vi.unstubAllEnvs();

    expect(doc.body.innerHTML).toBe("<p>on</p>");
  });
});
