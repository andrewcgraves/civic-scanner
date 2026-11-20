import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("next/server", () => ({ connection: () => Promise.resolve() }));
vi.mock("next/navigation", () => ({ usePathname: () => "/" }));
vi.mock("@/components/account-menu", () => ({
  AccountMenu: (props: Record<string, unknown>) => (
    <b data-props={JSON.stringify(props)}>account menu</b>
  ),
}));

const WORKOS = {
  WORKOS_CLIENT_ID: "client_01TEST",
  WORKOS_COOKIE_PASSWORD: "cookie-password-cookie-password-0",
  NEXT_PUBLIC_WORKOS_REDIRECT_URI: "https://civic.example/auth/callback",
};

const NO_WORKOS = {
  WORKOS_CLIENT_ID: undefined,
  WORKOS_COOKIE_PASSWORD: undefined,
  NEXT_PUBLIC_WORKOS_REDIRECT_URI: undefined,
};

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
    vi.restoreAllMocks();
  });

  async function renderShell() {
    const { default: RootLayout } = await import("./layout");
    return new DOMParser().parseFromString(
      renderToStaticMarkup(await RootLayout({ children: <p>page content</p> })),
      "text/html",
    );
  }

  it("wraps the page in an English document", async () => {
    const doc = await renderShell();

    expect(doc.documentElement.getAttribute("lang")).toBe("en");
  });

  it("puts the page alone inside the one main landmark", async () => {
    const doc = await renderShell();

    const mains = doc.querySelectorAll("main");
    expect(mains).toHaveLength(1);
    expect(mains[0]?.id).toBe("main");
    expect(mains[0]?.getAttribute("tabindex")).toBe("-1");
    expect(mains[0]?.innerHTML).toBe("<p>page content</p>");
  });

  it("starts with a skip link to the main content", async () => {
    const doc = await renderShell();

    const skip = doc.body.querySelector("a");
    expect(skip?.textContent).toBe("Skip to main content");
    expect(skip?.getAttribute("href")).toBe("#main");
  });

  it("has a header with the site name linking home and the primary navigation", async () => {
    const doc = await renderShell();

    const header = doc.querySelector("body > header");
    const home = header?.querySelector('a[href="/"]');
    expect(home?.textContent).toBe("Civic Scanner");
    const nav = header?.querySelector('nav[aria-label="Primary"]');
    expect(nav?.querySelector('a[href="/agencies"]')?.textContent).toBe(
      "Agencies",
    );
  });

  it("has a footer saying what the service is and that summaries are AI-written", async () => {
    const doc = await renderShell();

    const footer = doc.querySelector("body > footer")?.textContent ?? "";
    expect(footer).toContain("public meetings of Southern California");
    expect(footer).toContain("Summaries are written by AI");
    expect(footer).toContain("official record");
  });

  it("sets the site title", async () => {
    const { metadata } = await import("./layout");

    expect(metadata.title).toEqual({
      default: "Civic Scanner",
      template: "%s | Civic Scanner",
    });
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

  describe("sign-in", () => {
    it("renders exactly the page when WorkOS is not configured", async () => {
      const doc = await renderLayoutWithFlags({
        ...NO_WORKOS,
        NEXT_PUBLIC_POSTHOG_KEY: undefined,
        NEXT_PUBLIC_FEATURE_FLAGS: "accounts",
      });

      expect(doc.querySelector("header b")).toBeNull();
      expect(doc.querySelector("main")?.innerHTML).toBe("<p>off</p>");
    });

    it("has no account menu when the accounts flag is off", async () => {
      const doc = await renderLayoutWithFlags({
        ...WORKOS,
        NEXT_PUBLIC_POSTHOG_KEY: undefined,
        NEXT_PUBLIC_FEATURE_FLAGS: undefined,
      });

      expect(doc.querySelector("header b")).toBeNull();
    });

    it("has no account menu when the public redirect URI is not valid", async () => {
      const doc = await renderLayoutWithFlags({
        ...WORKOS,
        NEXT_PUBLIC_WORKOS_REDIRECT_URI: "http://civic.example/auth/callback",
        NEXT_PUBLIC_POSTHOG_KEY: undefined,
        NEXT_PUBLIC_FEATURE_FLAGS: "accounts",
      });

      expect(doc.querySelector("header b")).toBeNull();
    });

    it("reserves the header without the server secrets, so a build without them still offers sign-in", async () => {
      const doc = await renderLayoutWithFlags({
        ...NO_WORKOS,
        NEXT_PUBLIC_WORKOS_REDIRECT_URI: WORKOS.NEXT_PUBLIC_WORKOS_REDIRECT_URI,
        NEXT_PUBLIC_POSTHOG_KEY: undefined,
        NEXT_PUBLIC_FEATURE_FLAGS: "accounts",
      });

      expect(doc.querySelector("header b")?.textContent).toBe("account menu");
    });

    it("reserves one line of height for the account menu in every state", async () => {
      const doc = await renderLayoutWithFlags({
        ...WORKOS,
        NEXT_PUBLIC_POSTHOG_KEY: undefined,
        NEXT_PUBLIC_FEATURE_FLAGS: "accounts",
      });

      const classes = doc.querySelector("header b")?.parentElement?.classList;
      expect(classes?.contains("h-6")).toBe(true);
      expect(classes?.contains("items-center")).toBe(true);
    });

    it("puts the account menu in the header, with no props", async () => {
      const doc = await renderLayoutWithFlags({
        ...WORKOS,
        NEXT_PUBLIC_POSTHOG_KEY: undefined,
        NEXT_PUBLIC_FEATURE_FLAGS: "accounts",
      });

      const menu = doc.querySelector("header b");
      expect(menu?.textContent).toBe("account menu");
      expect(menu?.getAttribute("data-props")).toBe("{}");
      expect(doc.body.innerHTML).not.toContain(WORKOS.WORKOS_COOKIE_PASSWORD);
      expect(doc.body.innerHTML).not.toContain(WORKOS.WORKOS_CLIENT_ID);
    });
  });

  it("gives client components the in-memory flags", async () => {
    const doc = await renderLayoutWithFlags({
      NEXT_PUBLIC_POSTHOG_KEY: undefined,
      NEXT_PUBLIC_FEATURE_FLAGS: "test-only-flag",
    });

    expect(doc.querySelector("main")?.innerHTML).toBe("<p>on</p>");
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

    expect(doc.querySelector("main")?.innerHTML).toBe("<p>on</p>");
  });
});
