import { afterEach, describe, expect, it, vi } from "vitest";

const next = vi.hoisted(() => ({
  connection: vi.fn(() => Promise.resolve()),
  notFound: vi.fn(() => {
    throw new Error("NEXT_NOT_FOUND");
  }),
}));

vi.mock("next/server", () => ({ connection: next.connection }));
vi.mock("next/navigation", () => ({ notFound: next.notFound }));

const AGENCY = {
  id: "6e37b2a6-08c1-4ac7-9374-a859aa9fb342",
  slug: "la-metro",
  name: "LA Metro",
  website: "https://www.metro.net",
};

const MEETING_ID = "ad3d45f8-3ca9-4831-af75-0cd359f2f44a";

async function loadWith(respond: (request: Request) => Promise<Response>) {
  const fetchMock = vi.fn(respond);
  vi.stubGlobal("fetch", fetchMock);
  vi.resetModules();
  const loaders = await import("./load");
  const requests = () => fetchMock.mock.calls.map(([request]) => request);
  return { ...loaders, requests };
}

const ok = (body: unknown) => () => Promise.resolve(Response.json(body));

const status = (code: number) => () =>
  Promise.resolve(Response.json({ code: "x", message: "x" }, { status: code }));

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  next.connection.mockClear();
});

describe("API loaders", () => {
  it("load the agencies, uncached and dynamic", async () => {
    const { loadAgencies, requests } = await loadWith(
      ok({ agencies: [AGENCY] }),
    );

    expect(await loadAgencies()).toEqual({ ok: true, data: [AGENCY] });
    const [request] = requests();
    expect(request?.url).toBe("http://localhost:8080/v1/agencies");
    expect(request?.cache).toBe("no-store");
    expect(request?.signal).toBeInstanceOf(AbortSignal);
    expect(next.connection).toHaveBeenCalled();
  });

  it("load one agency by slug", async () => {
    const { loadAgency, requests } = await loadWith(ok(AGENCY));

    expect(await loadAgency("la-metro")).toEqual({ ok: true, data: AGENCY });
    expect(requests()[0]?.url).toBe(
      "http://localhost:8080/v1/agencies/la-metro",
    );
  });

  it("load a page of an agency's meetings with cursor and limit", async () => {
    const page = { meetings: [], nextCursor: null };
    const { loadAgencyMeetings, requests } = await loadWith(ok(page));

    expect(await loadAgencyMeetings("la-metro", "abc", 20)).toEqual({
      ok: true,
      data: page,
    });
    expect(requests()[0]?.url).toBe(
      "http://localhost:8080/v1/agencies/la-metro/meetings?cursor=abc&limit=20",
    );
  });

  it("load the first page of meetings without query parameters", async () => {
    const { loadAgencyMeetings, requests } = await loadWith(
      ok({ meetings: [], nextCursor: null }),
    );

    await loadAgencyMeetings("la-metro");

    expect(requests()[0]?.url).toBe(
      "http://localhost:8080/v1/agencies/la-metro/meetings",
    );
  });

  it("load a meeting by id", async () => {
    const meeting = { id: MEETING_ID };
    const { loadMeeting, requests } = await loadWith(ok(meeting));

    expect(await loadMeeting(MEETING_ID)).toEqual({ ok: true, data: meeting });
    expect(requests()[0]?.url).toBe(
      `http://localhost:8080/v1/meetings/${MEETING_ID}`,
    );
  });

  it.each([404, 400])("call notFound on a %i", async (code) => {
    const { loadMeeting } = await loadWith(status(code));

    await expect(loadMeeting(MEETING_ID)).rejects.toThrow("NEXT_NOT_FOUND");
  });

  it.each(["", "LA-Metro", "../me", "a".repeat(101)])(
    "call notFound for the slug %j without a request",
    async (slug) => {
      const { loadAgency, loadAgencyMeetings, requests } = await loadWith(
        ok(AGENCY),
      );

      await expect(loadAgency(slug)).rejects.toThrow("NEXT_NOT_FOUND");
      await expect(loadAgencyMeetings(slug)).rejects.toThrow("NEXT_NOT_FOUND");
      expect(requests()).toHaveLength(0);
    },
  );

  it("call notFound for a meeting id that is not a UUID without a request", async () => {
    const { loadMeeting, requests } = await loadWith(ok({}));

    await expect(loadMeeting("../agencies")).rejects.toThrow("NEXT_NOT_FOUND");
    expect(requests()).toHaveLength(0);
  });

  it("report unavailable on a server error", async () => {
    const consoleError = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const { loadAgencies } = await loadWith(status(503));

    expect(await loadAgencies()).toEqual({ ok: false });
    expect(consoleError).toHaveBeenCalledWith(
      "API request failed: GET /agencies",
      503,
    );
  });

  it("report unavailable when the API is unreachable", async () => {
    const failure = new TypeError("fetch failed");
    const consoleError = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const { loadAgency } = await loadWith(() => Promise.reject(failure));

    expect(await loadAgency("la-metro")).toEqual({ ok: false });
    expect(consoleError).toHaveBeenCalledWith(
      "API request failed: GET /agencies/{slug}",
      failure,
    );
  });
});
