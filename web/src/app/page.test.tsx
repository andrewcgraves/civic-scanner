import { render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const flags = vi.hoisted(() => ({ on: [] as string[] }));
const comingSoon = vi.hoisted(() => ({
  ids: new Set<string>(),
  find: vi.fn(),
}));

vi.mock("next/server", () => ({ connection: () => Promise.resolve() }));
vi.mock("@/lib/flags/server", () => ({
  isFlagEnabled: (flag: string) => Promise.resolve(flags.on.includes(flag)),
}));
vi.mock("@/lib/api/coming-soon", () => ({
  findComingSoon: comingSoon.find,
}));

const agencies = [
  {
    id: "7f1c1f0e-5d1b-4c62-9d55-0d6b6c2d9a01",
    slug: "la-metro",
    name: "LA Metro",
    website: "https://www.metro.net",
  },
  {
    id: "0b6f3c55-8f8e-4a0e-9a55-2f4c1f9d7b02",
    slug: "metrolink",
    name: "Metrolink",
    website: "ftp://metrolinktrains.com",
  },
];

async function renderWithFetch(fetchImpl: typeof fetch) {
  const fetchMock = vi.fn(fetchImpl);
  vi.stubGlobal("fetch", fetchMock);
  vi.resetModules();
  const { default: HomePage } = await import("./page");
  render(await HomePage());
  return fetchMock;
}

function respondWithAgencies() {
  return renderWithFetch(() => Promise.resolve(Response.json({ agencies })));
}

function coverageSection() {
  return screen.getByRole("region", { name: "Agencies we cover" });
}

describe("HomePage", () => {
  beforeEach(() => {
    flags.on = [];
    comingSoon.ids = new Set();
    comingSoon.find.mockImplementation(() => Promise.resolve(comingSoon.ids));
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("has a title and description", async () => {
    const { metadata } = await import("./page");

    expect(metadata.title).toBeUndefined();
    expect(metadata.description).toMatch(/board meetings/);
  });

  it("explains what the service does and its limits", async () => {
    await respondWithAgencies();

    expect(
      screen.getByRole("heading", {
        level: 1,
        name: "Follow your transportation agency's board meetings",
      }),
    ).toBeVisible();
    const whatItDoes = screen.getByRole("region", {
      name: "What Civic Scanner does",
    });
    expect(whatItDoes).toHaveTextContent(
      "Every day, it checks each agency's website for board meetings, upcoming and past.",
    );
    expect(whatItDoes).toHaveTextContent(/agenda/);
    expect(whatItDoes).toHaveTextContent(/summary of each agenda with AI/);
    expect(whatItDoes).toHaveTextContent(/what changed/);
    expect(whatItDoes).toHaveTextContent(/Transcripts/);
    const goodToKnow = screen.getByRole("region", { name: "Good to know" });
    expect(goodToKnow).toHaveTextContent(/official record/);
    expect(goodToKnow).toHaveTextContent(/can be wrong/);
    expect(goodToKnow).toHaveTextContent(/only the agencies listed below/);
    expect(goodToKnow).toHaveTextContent(
      "Civic Scanner is independent and is not run by any of these agencies.",
    );
  });

  it("lists the agencies it covers", async () => {
    const fetchMock = await respondWithAgencies();

    const list = within(coverageSection()).getByRole("list", {
      name: "Agencies",
    });
    expect(within(list).getAllByRole("listitem")).toHaveLength(2);
    expect(within(list).getByText("LA Metro")).toBeInTheDocument();
    expect(within(list).getByText("Metrolink")).toBeInTheDocument();
    const [request] = fetchMock.mock.calls[0] ?? [];
    expect((request as Request).url).toBe("http://localhost:8080/v1/agencies");
  });

  it("marks agencies without meetings as coming soon, still linked", async () => {
    flags.on = ["agency-pages"];
    comingSoon.ids = new Set([agencies[1]!.id]);
    await respondWithAgencies();

    expect(comingSoon.find).toHaveBeenCalledWith(agencies);
    expect(
      screen.getByText(/Agencies marked "Coming soon" are not connected yet/),
    ).toBeVisible();
    const metrolink = screen.getByRole("link", { name: /Metrolink/ });
    expect(metrolink).toHaveAttribute("href", "/agencies/metrolink");
    expect(within(metrolink).getByText("Coming soon")).toBeVisible();
    expect(
      within(screen.getByRole("link", { name: /LA Metro/ })).queryByText(
        "Coming soon",
      ),
    ).not.toBeInTheDocument();
  });

  it("does not look for coming-soon agencies when agencies are unavailable", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    await renderWithFetch(() => Promise.reject(new TypeError("fetch failed")));

    expect(comingSoon.find).not.toHaveBeenCalled();
    expect(screen.queryByText(/Coming soon/)).not.toBeInTheDocument();
  });

  it("links agencies to their pages when agency pages are on", async () => {
    flags.on = ["agency-pages"];
    await respondWithAgencies();

    const section = coverageSection();
    expect(
      within(section).getByRole("link", { name: /LA Metro/ }),
    ).toHaveAttribute("href", "/agencies/la-metro");
    expect(
      within(section).getByRole("link", { name: "Metrolink" }),
    ).toHaveAttribute("href", "/agencies/metrolink");
  });

  it("links only to agency websites when agency pages are off", async () => {
    await respondWithAgencies();

    const links = within(coverageSection()).getAllByRole("link");
    expect(links).toHaveLength(1);
    expect(links[0]).toHaveAttribute("href", "https://www.metro.net");
    expect(
      screen.queryByRole("link", { name: /Metrolink/ }),
    ).not.toBeInTheDocument();
  });

  it("shows the empty state when there are no agencies", async () => {
    await renderWithFetch(() =>
      Promise.resolve(Response.json({ agencies: [] })),
    );

    expect(coverageSection()).toHaveTextContent("No agencies are covered yet.");
  });

  it("keeps the explanation and shows a notice when agencies are unavailable", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    await renderWithFetch(() => Promise.reject(new TypeError("fetch failed")));

    expect(
      screen.getByRole("region", { name: "What Civic Scanner does" }),
    ).toBeVisible();
    expect(within(coverageSection()).getByRole("alert")).toHaveTextContent(
      "Agencies are unavailable right now. Please try again later.",
    );
  });
});
