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

const MEETING = {
  id: MEETING_ID,
  agency: AGENCY,
  title: "Regular Board Meeting",
  startsAt: "2027-01-20T17:00:00Z",
  status: "scheduled",
  agenda: null,
  recordings: [],
};

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
    const { loadMeeting, requests } = await loadWith(ok(MEETING));

    expect(await loadMeeting(MEETING_ID)).toEqual({ ok: true, data: MEETING });
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

  it("call notFound for an uppercase meeting id without a request", async () => {
    const { loadMeeting, requests } = await loadWith(ok(MEETING));

    await expect(loadMeeting(MEETING_ID.toUpperCase())).rejects.toThrow(
      "NEXT_NOT_FOUND",
    );
    expect(requests()).toHaveLength(0);
  });

  it.each([
    ["agencies", { agencies: null }],
    ["meeting agency", { ...MEETING, agency: null }],
    ["meeting recordings", { ...MEETING, recordings: null }],
    ["meeting date", { ...MEETING, startsAt: "not a date" }],
    [
      "agenda fields",
      {
        ...MEETING,
        agenda: {
          version: 1,
          documentUrl: null,
          summary: 42,
          changeNote: null,
          foundAt: "2027-01-13T18:30:00Z",
        },
      },
    ],
  ])("report unavailable for malformed %s", async (_what, body) => {
    const consoleError = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const { loadAgencies, loadMeeting } = await loadWith(ok(body));

    const result =
      "agencies" in body ? await loadAgencies() : await loadMeeting(MEETING_ID);

    expect(result).toEqual({ ok: false });
    expect(consoleError).toHaveBeenCalledWith(
      expect.stringMatching(/^API request failed: GET /),
      "unexpected response shape",
    );
  });

  it("give each request a timeout of REQUEST_TIMEOUT_MS", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const timeout = new AbortController();
    const timeoutSpy = vi
      .spyOn(AbortSignal, "timeout")
      .mockReturnValue(timeout.signal);
    const { loadAgency, REQUEST_TIMEOUT_MS } = await loadWith(
      (request) =>
        new Promise((_resolve, reject) => {
          request.signal.addEventListener("abort", () => {
            reject(request.signal.reason as Error);
          });
          timeout.abort(new DOMException("timed out", "TimeoutError"));
        }),
    );

    expect(await loadAgency("la-metro")).toEqual({ ok: false });
    expect(timeoutSpy).toHaveBeenCalledWith(REQUEST_TIMEOUT_MS);
    expect(REQUEST_TIMEOUT_MS).toBe(10_000);
  });

  it("stop a request when the shared deadline passes", async () => {
    vi.spyOn(console, "error").mockImplementation(() => undefined);
    const deadline = new AbortController();
    deadline.abort(new DOMException("deadline", "TimeoutError"));
    const { loadAgencyMeetings, requests } = await loadWith((request) =>
      request.signal.aborted
        ? Promise.reject(request.signal.reason as Error)
        : ok({ meetings: [], nextCursor: null })(),
    );

    expect(
      await loadAgencyMeetings("la-metro", "c1", undefined, deadline.signal),
    ).toEqual({ ok: false });
    expect(requests()).toHaveLength(1);
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
