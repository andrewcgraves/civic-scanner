import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

async function renderWithFetch(fetchImpl: typeof fetch) {
  const fetchMock = vi.fn(fetchImpl);
  vi.stubGlobal("fetch", fetchMock);
  vi.resetModules();
  const { default: AgenciesPage } = await import("./page");
  render(await AgenciesPage());
  return fetchMock;
}

const alertText = "Agencies are unavailable right now. Please try again later.";

describe("AgenciesPage", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("lists agencies from the API", async () => {
    const fetchMock = await renderWithFetch(() =>
      Promise.resolve(
        Response.json({
          agencies: [
            {
              id: "7f1c1f0e-5d1b-4c62-9d55-0d6b6c2d9a01",
              slug: "la-metro",
              name: "LA Metro",
              website: "https://www.metro.net",
            },
          ],
        }),
      ),
    );

    expect(screen.getByRole("heading", { name: "Agencies" })).toBeVisible();
    expect(screen.getByText("LA Metro")).toBeInTheDocument();
    const [request] = fetchMock.mock.calls[0] ?? [];
    expect(request).toBeInstanceOf(Request);
    expect((request as Request).url).toBe("http://localhost:8080/v1/agencies");
  });

  it("shows the empty state when there are no agencies", async () => {
    await renderWithFetch(() =>
      Promise.resolve(Response.json({ agencies: [] })),
    );

    expect(
      screen.getByText("No agencies are tracked yet."),
    ).toBeInTheDocument();
  });

  it("shows an alert when the API returns an error", async () => {
    await renderWithFetch(() =>
      Promise.resolve(
        Response.json(
          { code: "internal_error", message: "internal server error" },
          { status: 500 },
        ),
      ),
    );

    expect(screen.getByRole("alert")).toHaveTextContent(alertText);
  });

  it("shows an alert when the API is unreachable", async () => {
    await renderWithFetch(() => Promise.reject(new TypeError("fetch failed")));

    expect(screen.getByRole("alert")).toHaveTextContent(alertText);
  });
});
