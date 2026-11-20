import { beforeEach, describe, expect, it, vi } from "vitest";

import type { UserApiClient, UserApiResult } from "@/lib/auth/api";

const mocks = vi.hoisted(() => {
  const failed = (): UserApiResult<unknown> => ({ status: "failed" });
  return {
    flagsOn: new Set<string>(),
    signIn: true,
    result: failed(),
    calls: [] as unknown[],
    redirect: vi.fn((path: string) => {
      throw new Error(`NEXT_REDIRECT ${path}`);
    }),
  };
});

vi.mock("next/navigation", () => ({ redirect: mocks.redirect }));
vi.mock("@/lib/flags/server", () => ({
  isFlagEnabled: (flag: string) => Promise.resolve(mocks.flagsOn.has(flag)),
}));
vi.mock("@/lib/auth/server", () => ({
  isSignInEnabled: () => Promise.resolve(mocks.signIn),
}));
vi.mock("@/lib/auth/api", () => ({
  callApiAsUser: async (
    call: (client: UserApiClient) => Promise<unknown>,
  ): Promise<UserApiResult<unknown>> => {
    const client = {
      POST: (path: string, init: unknown) => {
        mocks.calls.push({ path, init });
        return Promise.resolve({ response: new Response() });
      },
    } as unknown as UserApiClient;
    await call(client);
    return mocks.result;
  },
}));

const { submitAgency } = await import("./actions");
const { INITIAL_STATE } = await import("./state");

const VALUES = { name: "Big Bear Transit", website: "bigbeartransit.org" };

function form(values: Record<string, string> = VALUES): FormData {
  const data = new FormData();
  for (const [key, value] of Object.entries(values)) {
    data.append(key, value);
  }
  return data;
}

describe("submitAgency", () => {
  beforeEach(() => {
    mocks.flagsOn = new Set(["agency-submissions"]);
    mocks.signIn = true;
    mocks.result = { status: "failed" };
    mocks.calls = [];
    mocks.redirect.mockClear();
  });

  it("posts the fields as typed and reports the stored submission", async () => {
    mocks.result = {
      status: "ok",
      data: {
        id: "x",
        name: "Big Bear Transit",
        website: "https://bigbeartransit.org",
        status: "pending",
        submittedAt: "2026-11-19T00:00:00Z",
      },
    };

    expect(await submitAgency(INITIAL_STATE, form())).toEqual({
      kind: "submitted",
      name: "Big Bear Transit",
      website: "https://bigbeartransit.org",
    });
    expect(mocks.calls).toEqual([
      { path: "/agency-submissions", init: { body: VALUES } },
    ]);
  });

  it("treats a malformed success as unavailable", async () => {
    mocks.result = { status: "ok", data: { name: 1 } };

    expect(await submitAgency(INITIAL_STATE, form())).toEqual({
      kind: "unavailable",
      values: VALUES,
    });
  });

  it.each([
    ["the flag is off", () => (mocks.flagsOn = new Set())],
    ["sign-in is off", () => (mocks.signIn = false)],
  ])("is closed when %s, without calling the API", async (_name, setUp) => {
    setUp();

    expect(await submitAgency(INITIAL_STATE, form())).toEqual({
      kind: "closed",
      values: VALUES,
    });
    expect(mocks.calls).toEqual([]);
  });

  it("checks required fields before calling the API", async () => {
    expect(
      await submitAgency(INITIAL_STATE, form({ name: " ", website: "x.org" })),
    ).toEqual({
      kind: "invalid",
      values: { name: " ", website: "x.org" },
      errors: { name: "Enter the agency's name." },
    });
    expect(mocks.calls).toEqual([]);
  });

  it("sends a signed-out visitor to sign in and back", async () => {
    mocks.result = { status: "signed-out" };

    await expect(submitAgency(INITIAL_STATE, form())).rejects.toThrow(
      "NEXT_REDIRECT /auth/sign-in?returnTo=%2Fsubmit-agency",
    );
  });

  it.each<[string, UserApiResult<unknown>, unknown]>([
    ["not found", { status: "not-found" }, { kind: "closed", values: VALUES }],
    [
      "unavailable",
      { status: "unavailable" },
      { kind: "unavailable", values: VALUES },
    ],
    ["failed", { status: "failed" }, { kind: "unavailable", values: VALUES }],
    [
      "a 409",
      { status: "conflict", error: { code: "already_on_file" } },
      { kind: "on-file", values: VALUES },
    ],
    [
      "a 429",
      { status: "rejected", httpStatus: 429, error: {} },
      { kind: "limit", values: VALUES },
    ],
    [
      "a 400 with fields",
      {
        status: "rejected",
        httpStatus: 400,
        error: {
          code: "invalid_submission",
          fields: [
            { field: "website", code: "ip_address", message: "No IPs." },
          ],
        },
      },
      { kind: "invalid", values: VALUES, errors: { website: "No IPs." } },
    ],
    [
      "a 415",
      {
        status: "rejected",
        httpStatus: 415,
        error: { code: "unsupported_media_type" },
      },
      {
        kind: "invalid",
        values: VALUES,
        errors: {},
        message: "Check the name and website and try again.",
      },
    ],
    [
      "a 413",
      {
        status: "rejected",
        httpStatus: 413,
        error: { code: "body_too_large" },
      },
      {
        kind: "invalid",
        values: VALUES,
        errors: {},
        message: "Check the name and website and try again.",
      },
    ],
  ])("maps %s", async (_name, result, want) => {
    mocks.result = result;

    expect(await submitAgency(INITIAL_STATE, form())).toEqual(want);
  });
});
