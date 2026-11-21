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
  previewTemplate,
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
  template: null,
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
        values: { ...VALUES, template: "", prompt: "" },
      });
    },
  );

  it("checks the fields before calling the API", async () => {
    expect(
      await createSubscription(IDLE_FORM, form({ agency: "", url: "" })),
    ).toEqual({
      kind: "invalid",
      values: {
        agency: "",
        url: "",
        format: "standard",
        template: "",
        prompt: "",
      },
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
      values: { ...VALUES, format: "teams", template: "", prompt: "" },
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
      values: { ...VALUES, template: "", prompt: "" },
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
      values: { ...VALUES, template: "", prompt: "" },
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
      values: { ...VALUES, template: "", prompt: "" },
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
      values: { ...VALUES, template: "", prompt: "" },
    });
  });

  it("sends a signed-out user to sign in", async () => {
    mocks.result = { status: "signed-out" };

    await expect(createSubscription(IDLE_FORM, form(VALUES))).rejects.toThrow(
      "NEXT_REDIRECT /auth/sign-in?returnTo=%2Fdashboard%2Fsubscriptions",
    );
  });
});

describe("directed prompts in createSubscription", () => {
  const VALUES = { agency: "la-metro", url: URL, format: "standard" };

  function body() {
    return (mocks.calls[0] as { init: { body: unknown } } | undefined)?.init
      .body;
  }

  it("sends the prompt trimmed, with line breaks as the API keeps them", async () => {
    mocks.result = { status: "ok", data: { ...SUBSCRIPTION, secret: SECRET } };

    await createSubscription(
      IDLE_FORM,
      form({ ...VALUES, prompt: "  Fare changes\r\nand passes  " }),
    );

    expect(body()).toEqual({
      agency: "la-metro",
      url: URL,
      format: "standard",
      prompt: "Fare changes\nand passes",
    });
  });

  it.each([
    ["the field is blank", { prompt: "  \r\n " }],
    ["the form has no prompt field", {}],
  ])("sends no prompt when %s", async (_case, extra) => {
    mocks.result = { status: "ok", data: { ...SUBSCRIPTION, secret: SECRET } };

    await createSubscription(IDLE_FORM, form({ ...VALUES, ...extra }));

    expect(body()).toEqual({
      agency: "la-metro",
      url: URL,
      format: "standard",
    });
  });

  it("refuses a prompt over the limit before calling the API", async () => {
    const prompt = "a".repeat(512);

    expect(
      await createSubscription(IDLE_FORM, form({ ...VALUES, prompt })),
    ).toEqual({
      kind: "invalid",
      values: { ...VALUES, template: "", prompt },
      errors: {
        prompt: "The prompt can be at most 500 characters. Take out 12.",
      },
    });
    expect(mocks.calls).toEqual([]);
  });

  it("puts the API's prompt refusal next to the prompt", async () => {
    mocks.result = {
      status: "rejected",
      httpStatus: 400,
      error: { code: "prompts_unavailable", message: "not yet" },
    };

    const state = await createSubscription(
      IDLE_FORM,
      form({ ...VALUES, prompt: "Fare changes" }),
    );

    expect(state).toEqual({
      kind: "invalid",
      values: { ...VALUES, template: "", prompt: "Fare changes" },
      errors: {
        prompt:
          "Directed prompts are not available on your account right now. Clear the prompt to save without one.",
      },
    });
  });
});

describe("updateSubscription", () => {
  const VALUES = { id: ID, agency: "la-metro", url: URL, format: "slack" };
  const KEPT = {
    agency: "la-metro",
    url: URL,
    format: "slack",
    template: "",
    prompt: "",
  };

  it("removes the prompt when the prompt field is sent blank", async () => {
    mocks.result = { status: "ok", data: SUBSCRIPTION };

    await expect(
      updateSubscription(IDLE_FORM, form({ ...VALUES, prompt: "  " })),
    ).rejects.toThrow("NEXT_REDIRECT");
    expect(mocks.calls).toEqual([
      {
        method: "PATCH",
        path: "/webhook-subscriptions/{id}",
        init: {
          params: { path: { id: ID } },
          body: { url: URL, format: "slack", prompt: "" },
        },
      },
    ]);
  });

  it("sends a changed prompt trimmed", async () => {
    mocks.result = { status: "ok", data: SUBSCRIPTION };

    await expect(
      updateSubscription(
        IDLE_FORM,
        form({ ...VALUES, prompt: " Blue Line\r\n" }),
      ),
    ).rejects.toThrow("NEXT_REDIRECT");
    expect(mocks.calls).toMatchObject([
      { init: { body: { url: URL, format: "slack", prompt: "Blue Line" } } },
    ]);
  });

  it("checks the prompt before calling the API", async () => {
    expect(
      await updateSubscription(
        IDLE_FORM,
        form({ ...VALUES, prompt: "Fares\u0007" }),
      ),
    ).toEqual({
      kind: "invalid",
      values: { ...KEPT, prompt: "Fares\u0007" },
      errors: {
        prompt:
          "The prompt has a hidden character that cannot be used, often from pasting. Retype that part as plain text; line breaks and tabs are fine.",
      },
    });
    expect(mocks.calls).toEqual([]);
  });

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

describe("custom templates", () => {
  const TEMPLATE = '{\n  "title": "{{meeting.title}}"\n}';
  const CREATED = { ...SUBSCRIPTION, format: "custom", secret: SECRET };

  it("sends the template, with plain line breaks, only for custom", async () => {
    mocks.result = { status: "ok", data: CREATED };

    await createSubscription(
      IDLE_FORM,
      form({
        agency: "la-metro",
        url: URL,
        format: "custom",
        template: TEMPLATE.replace(/\n/g, "\r\n"),
      }),
    );
    await createSubscription(
      IDLE_FORM,
      form({
        agency: "la-metro",
        url: URL,
        format: "slack",
        template: TEMPLATE,
      }),
    );

    expect(mocks.calls).toMatchObject([
      { init: { body: { format: "custom", template: TEMPLATE } } },
      { init: { body: { format: "slack" } } },
    ]);
    expect(mocks.calls[1]).not.toHaveProperty("init.body.template");
  });

  it("checks for a template before calling the API", async () => {
    const state = await createSubscription(
      IDLE_FORM,
      form({ agency: "la-metro", url: URL, format: "custom", template: " " }),
    );

    expect(state).toMatchObject({
      kind: "invalid",
      errors: {
        template: "Write a template, or choose another message format.",
      },
    });
    expect(mocks.calls).toEqual([]);
  });

  it("refuses an oversized template on update before calling the API", async () => {
    const state = await updateSubscription(
      IDLE_FORM,
      form({
        id: ID,
        agency: "la-metro",
        url: URL,
        format: "custom",
        template: "x".repeat(8193),
      }),
    );

    expect(state).toMatchObject({
      kind: "invalid",
      errors: { template: "The template is 1 character too long." },
    });
    expect(mocks.calls).toEqual([]);
  });

  it("sends the template on update and keeps where the API's problem is", async () => {
    mocks.result = {
      status: "rejected",
      httpStatus: 400,
      error: {
        code: "unknown_placeholder",
        message:
          "unknown placeholder {{meeting.titel}} in `title`; the placeholders are agency.name",
      },
    };
    const template = '{"title": "{{meeting.titel}}"}';

    const state = await updateSubscription(
      IDLE_FORM,
      form({
        id: ID,
        agency: "la-metro",
        url: URL,
        format: "custom",
        template,
      }),
    );

    expect(mocks.calls).toMatchObject([
      { init: { body: { format: "custom", template } } },
    ]);
    expect(state).toMatchObject({
      kind: "invalid",
      errors: {
        template: "{{meeting.titel}} in “title” is not a placeholder.",
      },
      templateProblem: {
        placeholder: "{{meeting.titel}}",
        suggestion: "{{meeting.title}}",
        index: 11,
      },
    });
  });
});

describe("previewTemplate", () => {
  const TEMPLATE = '{"title": "{{meeting.title}}"}';

  it("renders against the chosen event and agency", async () => {
    mocks.result = {
      status: "ok",
      data: { type: "agenda_changed", body: '{"title":"Sample"}' },
    };

    expect(
      await previewTemplate({
        template: TEMPLATE,
        type: "agenda_changed",
        agency: "la-metro",
      }),
    ).toEqual({
      kind: "rendered",
      type: "agenda_changed",
      body: '{"title":"Sample"}',
    });
    expect(mocks.calls).toEqual([
      {
        method: "POST",
        path: "/webhook-templates/preview",
        init: {
          body: {
            template: TEMPLATE,
            type: "agenda_changed",
            agency: "la-metro",
          },
        },
      },
    ]);
  });

  it("falls back to the default event and the sample agency", async () => {
    mocks.result = {
      status: "ok",
      data: { type: "agenda_published", body: "{}" },
    };

    await previewTemplate({
      template: TEMPLATE,
      type: "x",
      agency: "Not A Slug",
    });

    expect(mocks.calls).toMatchObject([
      { init: { body: { template: TEMPLATE, type: "agenda_published" } } },
    ]);
    expect(mocks.calls[0]).not.toHaveProperty("init.body.agency");
  });

  it("calls nothing for an empty or oversized template", async () => {
    expect(
      await previewTemplate({ template: " ", type: "", agency: "" }),
    ).toEqual({
      kind: "invalid",
      problem: {
        message: "Write a template, or choose another message format.",
      },
    });
    expect(
      await previewTemplate({
        template: "x".repeat(8200),
        type: "",
        agency: "",
      }),
    ).toEqual({
      kind: "invalid",
      problem: { message: "The template is 8 characters too long." },
    });
    expect(mocks.calls).toEqual([]);
  });

  it("turns a template refusal into a problem", async () => {
    mocks.result = {
      status: "rejected",
      httpStatus: 400,
      error: {
        code: "invalid_template",
        message: "the template must be a JSON object",
      },
    };

    expect(
      await previewTemplate({ template: "[]", type: "", agency: "" }),
    ).toEqual({
      kind: "invalid",
      problem: { message: "The template must be a JSON object." },
    });
  });

  it.each([
    { status: "rejected", httpStatus: 400, error: { code: "unknown_agency" } },
    { status: "payment-required" },
    { status: "signed-out" },
    { status: "failed" },
  ] as UserApiResult<unknown>[])("is unavailable for %j", async (result) => {
    mocks.result = result;

    expect(
      await previewTemplate({ template: TEMPLATE, type: "", agency: "" }),
    ).toEqual({ kind: "unavailable" });
    expect(mocks.redirect).not.toHaveBeenCalled();
  });

  it("calls nothing when subscriptions are off", async () => {
    mocks.enabled = false;

    expect(
      await previewTemplate({ template: TEMPLATE, type: "", agency: "" }),
    ).toEqual({ kind: "unavailable" });
    expect(mocks.calls).toEqual([]);
  });
});
