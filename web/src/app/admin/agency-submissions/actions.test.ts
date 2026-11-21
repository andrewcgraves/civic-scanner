import { beforeEach, describe, expect, it, vi } from "vitest";

import type { UserApiClient, UserApiResult } from "@/lib/auth/api";

const mocks = vi.hoisted(() => {
  const failed = (): UserApiResult<unknown> => ({ status: "failed" });
  return {
    signIn: true,
    result: failed(),
    calls: [] as unknown[],
    redirect: vi.fn((path: string) => {
      throw new Error(`NEXT_REDIRECT ${path}`);
    }),
  };
});

vi.mock("next/navigation", () => ({ redirect: mocks.redirect }));
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

const { approveSubmission, rejectSubmission } = await import("./actions");
const { SLUG_RULE } = await import("./state");

const ID = "8b0e6c43-3f3b-4f39-9a31-2a1f7c1a9e10";
const IDLE = { kind: "idle", slug: "" } as const;

function form(values: Record<string, string>): FormData {
  const data = new FormData();
  for (const [key, value] of Object.entries(values)) {
    data.append(key, value);
  }
  return data;
}

const REVIEWED = {
  id: ID,
  slug: "big-bear",
  name: "Big Bear Transit",
  website: "https://bigbear.example.org",
  status: "live",
};

describe("approveSubmission", () => {
  beforeEach(() => {
    mocks.signIn = true;
    mocks.result = { status: "failed" };
    mocks.calls = [];
    mocks.redirect.mockClear();
  });

  it("approves with the slug and returns to the list", async () => {
    mocks.result = { status: "ok", data: REVIEWED };

    await expect(
      approveSubmission(IDLE, form({ id: ID, slug: " big-bear " })),
    ).rejects.toThrow(
      "NEXT_REDIRECT /admin/agency-submissions?approved=big-bear",
    );
    expect(mocks.calls).toEqual([
      {
        path: "/admin/agency-submissions/{id}/approve",
        init: { params: { path: { id: ID } }, body: { slug: "big-bear" } },
      },
    ]);
  });

  it("checks the slug before calling the API", async () => {
    expect(
      await approveSubmission(IDLE, form({ id: ID, slug: "Big Bear" })),
    ).toEqual({ kind: "invalid-slug", slug: "Big Bear", message: SLUG_RULE });
    expect(mocks.calls).toEqual([]);
  });

  it("refuses an id that is not a UUID without calling the API", async () => {
    expect(
      await approveSubmission(IDLE, form({ id: "../me", slug: "big-bear" })),
    ).toEqual({ kind: "gone", slug: "big-bear" });
    expect(mocks.calls).toEqual([]);
  });

  it("does nothing with sign-in off", async () => {
    mocks.signIn = false;
    expect(
      await approveSubmission(IDLE, form({ id: ID, slug: "big-bear" })),
    ).toEqual({ kind: "not-allowed", slug: "big-bear" });
    expect(mocks.calls).toEqual([]);
  });

  it.each<[string, UserApiResult<unknown>, unknown]>([
    [
      "a taken slug",
      { status: "conflict", error: { code: "slug_taken", message: "x" } },
      { kind: "slug-taken", slug: "big-bear" },
    ],
    [
      "an agency already reviewed",
      { status: "conflict", error: { code: "not_pending", message: "x" } },
      { kind: "reviewed", slug: "big-bear" },
    ],
    [
      "a slug the API refuses",
      {
        status: "rejected",
        httpStatus: 400,
        error: { code: "invalid_slug", message: "the slug is bad" },
      },
      { kind: "invalid-slug", slug: "big-bear", message: "the slug is bad" },
    ],
    [
      "a 400 without a message",
      { status: "rejected", httpStatus: 400, error: null },
      { kind: "invalid-slug", slug: "big-bear", message: SLUG_RULE },
    ],
    [
      "a 413",
      { status: "rejected", httpStatus: 413, error: null },
      { kind: "unavailable", slug: "big-bear" },
    ],
    [
      "an unknown agency",
      { status: "not-found" },
      { kind: "gone", slug: "big-bear" },
    ],
    [
      "a demoted admin",
      { status: "forbidden" },
      { kind: "not-allowed", slug: "big-bear" },
    ],
    [
      "an unavailable API",
      { status: "unavailable" },
      { kind: "unavailable", slug: "big-bear" },
    ],
    [
      "a failure",
      { status: "failed" },
      { kind: "unavailable", slug: "big-bear" },
    ],
  ])("reports %s", async (_name, result, want) => {
    mocks.result = result;
    expect(
      await approveSubmission(IDLE, form({ id: ID, slug: "big-bear" })),
    ).toEqual(want);
  });

  it("sends a signed-out admin to sign in", async () => {
    mocks.result = { status: "signed-out" };
    await expect(
      approveSubmission(IDLE, form({ id: ID, slug: "big-bear" })),
    ).rejects.toThrow(
      "NEXT_REDIRECT /auth/sign-in?returnTo=%2Fadmin%2Fagency-submissions",
    );
  });
});

describe("rejectSubmission", () => {
  beforeEach(() => {
    mocks.signIn = true;
    mocks.result = { status: "failed" };
    mocks.calls = [];
  });

  it("rejects and returns to the list", async () => {
    mocks.result = { status: "ok", data: { ...REVIEWED, status: "rejected" } };

    await expect(rejectSubmission(IDLE, form({ id: ID }))).rejects.toThrow(
      "NEXT_REDIRECT /admin/agency-submissions?rejected=1",
    );
    expect(mocks.calls).toEqual([
      {
        path: "/admin/agency-submissions/{id}/reject",
        init: { params: { path: { id: ID } } },
      },
    ]);
  });

  it("refuses an id that is not a UUID", async () => {
    expect(await rejectSubmission(IDLE, form({ id: "x" }))).toEqual({
      kind: "gone",
      slug: "",
    });
    expect(mocks.calls).toEqual([]);
  });

  it("does nothing with sign-in off", async () => {
    mocks.signIn = false;
    expect(await rejectSubmission(IDLE, form({ id: ID }))).toEqual({
      kind: "not-allowed",
      slug: "",
    });
  });

  it("reports an agency already reviewed", async () => {
    mocks.result = {
      status: "conflict",
      error: { code: "not_pending", message: "x" },
    };
    expect(await rejectSubmission(IDLE, form({ id: ID }))).toEqual({
      kind: "reviewed",
      slug: "",
    });
  });
});
