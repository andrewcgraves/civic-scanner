import { beforeEach, describe, expect, it, vi } from "vitest";

import type { UserApiClient, UserApiResult } from "@/lib/auth/api";

const mocks = vi.hoisted(() => {
  const failed = (): UserApiResult<unknown> => ({ status: "failed" });
  const noHeaders = (): Record<string, string> => ({});
  return {
    enabled: true,
    result: failed(),
    deleteStatus: 204,
    headers: noHeaders(),
    calls: [] as unknown[],
    answers: [] as unknown[],
    redirect: vi.fn((path: string) => {
      throw new Error(`NEXT_REDIRECT ${path}`);
    }),
  };
});

vi.mock("next/navigation", () => ({ redirect: mocks.redirect }));
vi.mock("./server", () => ({
  isSubscriptionsEnabled: () => Promise.resolve(mocks.enabled),
}));
vi.mock("@/lib/auth/api", () => ({
  callApiAsUser: async (
    call: (client: UserApiClient) => Promise<unknown>,
  ): Promise<UserApiResult<unknown>> => {
    const record =
      (method: string) =>
      (path: string, init: unknown): Promise<unknown> => {
        mocks.calls.push({ method, path, init });
        return Promise.resolve({
          response: new Response(null, {
            status: mocks.deleteStatus,
            headers: mocks.headers,
          }),
        });
      };
    const client = {
      POST: record("POST"),
      PATCH: record("PATCH"),
      DELETE: record("DELETE"),
    } as unknown as UserApiClient;
    mocks.answers.push(await call(client));
    return mocks.result;
  },
}));

const {
  createSubscription,
  deleteSubscription,
  pauseSubscription,
  resumeSubscription,
  rotateSecret,
  sendTest,
  updateSubscription,
} = await import("./actions");
const { IDLE_TEST } = await import("./deliveries");
const { FORM_ERROR, IDLE_ACTION, initialForm } = await import("./state");

const ID = "8b0e6c43-3f3b-4f39-9a31-2a1f7c1a9e10";
const IDLE_FORM = initialForm({});
const URL = "https://hooks.example.org/civic";
const SUBSCRIPTION = {
  id: ID,
  agency: "la-metro",
  url: URL,
  payloadVersion: "v1",
  format: "standard",
  status: "active",
  pausedReason: null,
  pausedAt: null,
  consecutiveFailedEvents: 0,
  createdAt: "2026-11-03T18:00:00Z",
  updatedAt: "2026-11-03T18:00:00Z",
  secretRotatedAt: "2026-11-03T18:00:00Z",
};
const SECRET = "whsec_c2VjcmV0";

function form(values: Record<string, string>): FormData {
  const data = new FormData();
  for (const [key, value] of Object.entries(values)) {
    data.append(key, value);
  }
  return data;
}

beforeEach(() => {
  mocks.enabled = true;
  mocks.result = { status: "failed" };
  mocks.deleteStatus = 204;
  mocks.headers = {};
  mocks.calls = [];
  mocks.answers = [];
  mocks.redirect.mockClear();
});

describe("createSubscription", () => {
  const VALUES = { agency: "la-metro", url: ` ${URL} `, format: "discord" };

  it("creates the subscription and hands back its secret once", async () => {
    mocks.result = { status: "ok", data: { ...SUBSCRIPTION, secret: SECRET } };

    expect(await createSubscription(IDLE_FORM, form(VALUES))).toEqual({
      kind: "created",
      agency: "la-metro",
      url: URL,
      secret: SECRET,
    });
    expect(mocks.calls).toEqual([
      {
        method: "POST",
        path: "/webhook-subscriptions",
        init: { body: { agency: "la-metro", url: URL, format: "discord" } },
      },
    ]);
  });

  it.each([null, { ...SUBSCRIPTION }, { ...SUBSCRIPTION, secret: "" }])(
    "is unavailable when the answer has no secret (%j)",
    async (data) => {
      mocks.result = { status: "ok", data };

      expect(await createSubscription(IDLE_FORM, form(VALUES))).toEqual({
        kind: "unavailable",
        values: VALUES,
      });
    },
  );

  it("checks the fields before calling the API", async () => {
    expect(
      await createSubscription(IDLE_FORM, form({ agency: "", url: "" })),
    ).toEqual({
      kind: "invalid",
      values: { agency: "", url: "", format: "standard" },
      errors: {
        agency: "Choose an agency.",
        url: "Enter the URL to send webhooks to.",
      },
    });
    expect(mocks.calls).toEqual([]);
  });

  it("refuses a format it does not offer", async () => {
    expect(
      await createSubscription(IDLE_FORM, form({ ...VALUES, format: "teams" })),
    ).toEqual({
      kind: "invalid",
      values: { ...VALUES, format: "teams" },
      errors: { format: "Choose a message format." },
    });
    expect(mocks.calls).toEqual([]);
  });

  it("sends the standard format when none is chosen", async () => {
    mocks.result = { status: "ok", data: { ...SUBSCRIPTION, secret: SECRET } };

    await createSubscription(IDLE_FORM, form({ agency: "la-metro", url: URL }));
    expect(mocks.calls).toEqual([
      {
        method: "POST",
        path: "/webhook-subscriptions",
        init: { body: { agency: "la-metro", url: URL, format: "standard" } },
      },
    ]);
  });

  it("calls nothing when subscriptions are off", async () => {
    mocks.enabled = false;

    expect(await createSubscription(IDLE_FORM, form(VALUES))).toEqual({
      kind: "gone",
      values: VALUES,
    });
    expect(mocks.calls).toEqual([]);
  });

  it("shows the API's URL problem next to the URL", async () => {
    mocks.result = {
      status: "rejected",
      httpStatus: 400,
      error: { code: "not_public", message: "the host is private" },
    };

    expect(await createSubscription(IDLE_FORM, form(VALUES))).toEqual({
      kind: "invalid",
      values: VALUES,
      errors: { url: "The host is private." },
    });
  });

  it("gives a form-level message for a body problem", async () => {
    mocks.result = {
      status: "rejected",
      httpStatus: 400,
      error: { code: "invalid_body", message: "x" },
    };

    expect(await createSubscription(IDLE_FORM, form(VALUES))).toEqual({
      kind: "invalid",
      values: VALUES,
      errors: {},
      message: FORM_ERROR,
    });
  });

  it.each<[UserApiResult<unknown>, string]>([
    [{ status: "rejected", httpStatus: 429, error: null }, "limit"],
    [{ status: "conflict", error: null }, "duplicate"],
    [{ status: "payment-required" }, "upgrade"],
    [{ status: "not-found" }, "gone"],
    [{ status: "unavailable" }, "unavailable"],
    [{ status: "forbidden" }, "unavailable"],
    [{ status: "failed" }, "unavailable"],
  ])("maps %j to %s and keeps the values", async (result, kind) => {
    mocks.result = result;

    expect(await createSubscription(IDLE_FORM, form(VALUES))).toEqual({
      kind,
      values: VALUES,
    });
  });

  it("sends a signed-out user to sign in", async () => {
    mocks.result = { status: "signed-out" };

    await expect(createSubscription(IDLE_FORM, form(VALUES))).rejects.toThrow(
      "NEXT_REDIRECT /auth/sign-in?returnTo=%2Fdashboard%2Fsubscriptions",
    );
  });
});

describe("updateSubscription", () => {
  const VALUES = { id: ID, agency: "la-metro", url: URL, format: "slack" };
  const KEPT = { agency: "la-metro", url: URL, format: "slack" };

  it("saves the new URL and returns to the list", async () => {
    mocks.result = { status: "ok", data: SUBSCRIPTION };

    await expect(updateSubscription(IDLE_FORM, form(VALUES))).rejects.toThrow(
      "NEXT_REDIRECT /dashboard/subscriptions?updated=la-metro",
    );
    expect(mocks.calls).toEqual([
      {
        method: "PATCH",
        path: "/webhook-subscriptions/{id}",
        init: {
          params: { path: { id: ID } },
          body: { url: URL, format: "slack" },
        },
      },
    ]);
  });

  it("returns to the plain list when the answer has an odd agency", async () => {
    mocks.result = { status: "ok", data: { ...SUBSCRIPTION, agency: "Odd" } };

    await expect(updateSubscription(IDLE_FORM, form(VALUES))).rejects.toThrow(
      "NEXT_REDIRECT /dashboard/subscriptions",
    );
    expect(mocks.redirect).toHaveBeenCalledWith("/dashboard/subscriptions");
  });

  it("checks the URL before calling the API", async () => {
    expect(
      await updateSubscription(IDLE_FORM, form({ ...VALUES, url: " " })),
    ).toEqual({
      kind: "invalid",
      values: { ...KEPT, url: " " },
      errors: { url: "Enter the URL to send webhooks to." },
    });
    expect(mocks.calls).toEqual([]);
  });

  it("checks the format before calling the API", async () => {
    expect(
      await updateSubscription(IDLE_FORM, form({ ...VALUES, format: "x" })),
    ).toEqual({
      kind: "invalid",
      values: { ...KEPT, format: "x" },
      errors: { format: "Choose a message format." },
    });
    expect(mocks.calls).toEqual([]);
  });

  it.each([
    ["subscriptions are off", false, ID],
    ["the id is not a UUID", true, "nope"],
  ])("is gone when %s", async (_name, enabled, id) => {
    mocks.enabled = enabled;

    expect(
      await updateSubscription(IDLE_FORM, form({ ...VALUES, id })),
    ).toEqual({ kind: "gone", values: KEPT });
    expect(mocks.calls).toEqual([]);
  });

  it("explains a failure and keeps the values", async () => {
    mocks.result = { status: "conflict", error: null };

    expect(await updateSubscription(IDLE_FORM, form(VALUES))).toEqual({
      kind: "duplicate",
      values: KEPT,
    });
  });
});

describe("pauseSubscription and resumeSubscription", () => {
  it("pauses and returns to the list", async () => {
    mocks.result = {
      status: "ok",
      data: { ...SUBSCRIPTION, status: "paused" },
    };

    await expect(
      pauseSubscription(IDLE_ACTION, form({ id: ID })),
    ).rejects.toThrow("NEXT_REDIRECT /dashboard/subscriptions?paused=la-metro");
    expect(mocks.calls).toEqual([
      {
        method: "PATCH",
        path: "/webhook-subscriptions/{id}",
        init: { params: { path: { id: ID } }, body: { status: "paused" } },
      },
    ]);
  });

  it("resumes and returns to the list", async () => {
    mocks.result = { status: "ok", data: SUBSCRIPTION };

    await expect(
      resumeSubscription(IDLE_ACTION, form({ id: ID })),
    ).rejects.toThrow(
      "NEXT_REDIRECT /dashboard/subscriptions?resumed=la-metro",
    );
    expect(mocks.calls).toEqual([
      {
        method: "PATCH",
        path: "/webhook-subscriptions/{id}",
        init: { params: { path: { id: ID } }, body: { status: "active" } },
      },
    ]);
  });

  it("returns to the subscription's page when asked", async () => {
    mocks.result = { status: "ok", data: SUBSCRIPTION };

    await expect(
      resumeSubscription(
        IDLE_ACTION,
        form({ id: ID, returnTo: "subscription" }),
      ),
    ).rejects.toThrow(
      `NEXT_REDIRECT /dashboard/subscriptions/${ID}?resumed=la-metro`,
    );
  });

  it("returns to the plain list when the answer has an odd agency", async () => {
    mocks.result = {
      status: "ok",
      data: { ...SUBSCRIPTION, agency: "Not A Slug" },
    };

    await expect(
      resumeSubscription(
        IDLE_ACTION,
        form({ id: ID, returnTo: "subscription" }),
      ),
    ).rejects.toThrow(/^NEXT_REDIRECT \/dashboard\/subscriptions$/);
  });

  it("is gone for a bad id, without calling the API", async () => {
    expect(await pauseSubscription(IDLE_ACTION, form({ id: "x" }))).toEqual({
      kind: "gone",
    });
    expect(mocks.calls).toEqual([]);
  });

  it.each<[UserApiResult<unknown>, string]>([
    [{ status: "not-found" }, "gone"],
    [{ status: "payment-required" }, "upgrade"],
    [{ status: "failed" }, "unavailable"],
    [{ status: "conflict", error: null }, "unavailable"],
  ])("maps %j to %s", async (result, kind) => {
    mocks.result = result;

    expect(await resumeSubscription(IDLE_ACTION, form({ id: ID }))).toEqual({
      kind,
    });
  });

  it("sends a signed-out user to sign in", async () => {
    mocks.result = { status: "signed-out" };

    await expect(
      pauseSubscription(IDLE_ACTION, form({ id: ID })),
    ).rejects.toThrow("NEXT_REDIRECT /auth/sign-in");
  });
});

describe("deleteSubscription", () => {
  it("deletes and returns to the list naming the agency", async () => {
    mocks.result = { status: "ok", data: true };

    await expect(
      deleteSubscription(IDLE_ACTION, form({ id: ID, agency: "la-metro" })),
    ).rejects.toThrow(
      "NEXT_REDIRECT /dashboard/subscriptions?deleted=la-metro",
    );
    expect(mocks.calls).toEqual([
      {
        method: "DELETE",
        path: "/webhook-subscriptions/{id}",
        init: { params: { path: { id: ID } } },
      },
    ]);
  });

  it("turns an empty 204 into data and an error into none", async () => {
    mocks.result = { status: "failed" };

    await deleteSubscription(IDLE_ACTION, form({ id: ID, agency: "x" }));
    mocks.deleteStatus = 500;
    await deleteSubscription(IDLE_ACTION, form({ id: ID, agency: "x" }));

    expect(mocks.answers).toMatchObject([{ data: true }, { data: undefined }]);
  });

  it("is gone when subscriptions are off", async () => {
    mocks.enabled = false;

    expect(
      await deleteSubscription(IDLE_ACTION, form({ id: ID, agency: "x" })),
    ).toEqual({ kind: "gone" });
  });

  it("explains a failure", async () => {
    mocks.result = { status: "not-found" };

    expect(
      await deleteSubscription(IDLE_ACTION, form({ id: ID, agency: "x" })),
    ).toEqual({ kind: "gone" });
  });
});

describe("rotateSecret", () => {
  it("rotates and hands back the new secret", async () => {
    mocks.result = { status: "ok", data: { ...SUBSCRIPTION, secret: SECRET } };

    expect(await rotateSecret(IDLE_ACTION, form({ id: ID }))).toEqual({
      kind: "rotated",
      secret: SECRET,
    });
    expect(mocks.calls).toEqual([
      {
        method: "POST",
        path: "/webhook-subscriptions/{id}/secret",
        init: { params: { path: { id: ID } } },
      },
    ]);
  });

  it("is unavailable when the answer has no secret", async () => {
    mocks.result = { status: "ok", data: SUBSCRIPTION };

    expect(await rotateSecret(IDLE_ACTION, form({ id: ID }))).toEqual({
      kind: "unavailable",
    });
  });

  it("is gone for a bad id", async () => {
    expect(await rotateSecret(IDLE_ACTION, form({}))).toEqual({
      kind: "gone",
    });
    expect(mocks.calls).toEqual([]);
  });

  it("explains a failure", async () => {
    mocks.result = { status: "payment-required" };

    expect(await rotateSecret(IDLE_ACTION, form({ id: ID }))).toEqual({
      kind: "upgrade",
    });
  });
});

describe("sendTest", () => {
  const RESULT = {
    id: "5b1c7d2e-0000-4000-8000-000000000001",
    type: "agenda_changed",
    sentAt: "2026-11-20T18:14:00Z",
    succeeded: true,
    statusCode: 200,
    durationMs: 182,
    error: null,
  };

  it("sends the chosen event type and hands back the result", async () => {
    mocks.result = { status: "ok", data: RESULT };

    expect(
      await sendTest(IDLE_TEST, form({ id: ID, type: "agenda_changed" })),
    ).toEqual({ kind: "sent", result: RESULT });
    expect(mocks.calls).toEqual([
      {
        method: "POST",
        path: "/webhook-subscriptions/{id}/test",
        init: {
          params: { path: { id: ID } },
          body: { type: "agenda_changed" },
        },
      },
    ]);
  });

  it("sends the default event type for an unknown one", async () => {
    mocks.result = { status: "ok", data: RESULT };

    await sendTest(IDLE_TEST, form({ id: ID, type: "meeting_moved" }));

    expect(mocks.calls).toEqual([
      expect.objectContaining({
        init: expect.objectContaining({
          body: { type: "agenda_published" },
        }) as unknown,
      }),
    ]);
  });

  it("reads Retry-After when the test limit is reached", async () => {
    mocks.result = { status: "rejected", httpStatus: 429, error: null };
    mocks.headers = { "Retry-After": "42" };
    mocks.deleteStatus = 429;

    expect(await sendTest(IDLE_TEST, form({ id: ID }))).toEqual({
      kind: "limited",
      retryAfter: 42,
    });
  });

  it("is gone for a bad id or when subscriptions are off", async () => {
    expect(await sendTest(IDLE_TEST, form({ id: "x" }))).toEqual({
      kind: "gone",
    });
    mocks.enabled = false;
    expect(await sendTest(IDLE_TEST, form({ id: ID }))).toEqual({
      kind: "gone",
    });
    expect(mocks.calls).toEqual([]);
  });

  it.each<[UserApiResult<unknown>, string]>([
    [{ status: "not-found" }, "gone"],
    [{ status: "payment-required" }, "upgrade"],
    [{ status: "rejected", httpStatus: 400, error: null }, "unavailable"],
    [{ status: "unavailable" }, "unavailable"],
  ])("maps %j to %s", async (result, kind) => {
    mocks.result = result;

    expect(await sendTest(IDLE_TEST, form({ id: ID }))).toEqual({ kind });
  });

  it("sends a signed-out user to sign in", async () => {
    mocks.result = { status: "signed-out" };

    await expect(sendTest(IDLE_TEST, form({ id: ID }))).rejects.toThrow(
      "NEXT_REDIRECT /auth/sign-in",
    );
  });
});
