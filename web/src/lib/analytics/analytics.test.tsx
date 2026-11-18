import { render } from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const router = vi.hoisted(() => ({
  pathname: "/",
  search: "",
}));

const vitals = vi.hoisted(() => ({
  callbacks: [] as unknown[],
}));

const posthog = vi.hoisted(() => ({
  init: vi.fn(),
  capture: vi.fn<(event: string, properties: object) => void>(),
}));

vi.mock("next/navigation", () => ({
  usePathname: () => router.pathname,
  useSearchParams: () => new URLSearchParams(router.search),
}));

vi.mock("next/web-vitals", () => ({
  useReportWebVitals: (callback: unknown) => {
    vitals.callbacks.push(callback);
  },
}));

vi.mock("posthog-js/dist/module.slim.no-external", () => ({
  default: posthog,
}));

const PROJECT = { key: "phc_test", host: "https://us.i.posthog.com" };

async function loadAnalytics() {
  vi.resetModules();
  const { Analytics } = await import("./analytics");
  const { reportWebVital } = await import("./client");
  return { Analytics, reportWebVital };
}

async function settle() {
  await vi.dynamicImportSettled();
  await new Promise((resolve) => setTimeout(resolve, 0));
}

function pageviews() {
  return posthog.capture.mock.calls
    .filter(([event]) => event === "$pageview")
    .map(([, properties]) => properties);
}

beforeEach(() => {
  router.pathname = "/";
  router.search = "";
  vitals.callbacks = [];
  posthog.init.mockReset();
  posthog.capture.mockReset();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("Analytics", () => {
  it("renders nothing", async () => {
    const { Analytics } = await loadAnalytics();

    const { container } = render(<Analytics project={PROJECT} />);

    expect(container).toBeEmptyDOMElement();
  });

  it("sends one page view for the first load, even in strict mode", async () => {
    const { Analytics } = await loadAnalytics();

    render(
      <StrictMode>
        <Analytics project={PROJECT} />
      </StrictMode>,
    );
    await settle();

    expect(posthog.init).toHaveBeenCalledOnce();
    expect(pageviews()).toEqual([
      { $current_url: "http://localhost:3000/", $pathname: "/" },
    ]);
  });

  it("sends one page view per client-side navigation to a new path", async () => {
    const { Analytics } = await loadAnalytics();
    const { rerender } = render(<Analytics project={PROJECT} />);

    router.pathname = "/agencies";
    rerender(<Analytics project={PROJECT} />);
    rerender(<Analytics project={PROJECT} />);
    router.search = "page=2";
    rerender(<Analytics project={PROJECT} />);
    await settle();

    expect(pageviews()).toEqual([
      { $current_url: "http://localhost:3000/", $pathname: "/" },
      {
        $current_url: "http://localhost:3000/agencies",
        $pathname: "/agencies",
      },
    ]);
  });

  it("reports web vitals through one stable callback", async () => {
    const { Analytics, reportWebVital } = await loadAnalytics();
    const { rerender } = render(<Analytics project={PROJECT} />);
    rerender(<Analytics project={PROJECT} />);

    expect(new Set(vitals.callbacks)).toEqual(new Set([reportWebVital]));
  });
});
