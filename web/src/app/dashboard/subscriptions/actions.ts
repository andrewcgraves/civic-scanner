"use server";

import { redirect } from "next/navigation";

import { callApiAsUser, type UserApiResult } from "@/lib/auth/api";

import {
  DEFAULT_EVENT_TYPE,
  isEventType,
  readRetryAfter,
  type TestState,
} from "./deliveries";
import { isSubscriptionsEnabled } from "./server";
import {
  SUBSCRIPTIONS_PATH,
  SUBSCRIPTIONS_SIGN_IN_PATH,
  checkFormat,
  checkPrompt,
  checkTemplate,
  checkUrl,
  checkValues,
  isCustom,
  isFormat,
  isSlug,
  isSubscriptionId,
  normalPrompt,
  outcomePath,
  readId,
  readReturnsToSubscription,
  readValues,
  readsPrompt,
  rejection,
  subscriptionOutcomePath,
  type ActionState,
  type FormState,
  type SubscriptionValues,
} from "./state";
import {
  localProblem,
  templateProblem,
  type PreviewState,
} from "./custom-template";

function formFailure(
  result: UserApiResult<unknown>,
  values: SubscriptionValues,
): FormState {
  switch (result.status) {
    case "signed-out":
      return redirect(SUBSCRIPTIONS_SIGN_IN_PATH);
    case "not-found":
      return { kind: "gone", values };
    case "conflict":
      return { kind: "duplicate", values };
    case "payment-required":
      return { kind: "upgrade", values };
    case "rejected":
      if (result.httpStatus === 429) {
        return { kind: "limit", values };
      }
      return {
        kind: "invalid",
        values,
        ...rejection(result.error, values.template),
      };
    default:
      return { kind: "unavailable", values };
  }
}

function actionFailure(result: UserApiResult<unknown>): ActionState {
  switch (result.status) {
    case "signed-out":
      return redirect(SUBSCRIPTIONS_SIGN_IN_PATH);
    case "not-found":
      return { kind: "gone" };
    case "payment-required":
      return { kind: "upgrade" };
    default:
      return { kind: "unavailable" };
  }
}

function createdPrompt(formData: FormData, values: SubscriptionValues) {
  const prompt = normalPrompt(values.prompt);
  return readsPrompt(formData) && prompt !== "" ? { prompt } : {};
}

function updatedPrompt(formData: FormData, values: SubscriptionValues) {
  return readsPrompt(formData) ? { prompt: normalPrompt(values.prompt) } : {};
}

function sentTemplate(values: SubscriptionValues) {
  return isCustom(values) ? { template: values.template } : {};
}

function hasSecret(data: unknown): data is { secret: string } {
  if (typeof data !== "object" || data === null) {
    return false;
  }
  const { secret } = data as { secret?: unknown };
  return typeof secret === "string" && secret !== "";
}

function doneAt(
  kind: "updated" | "paused" | "resumed" | "deleted",
  agency: string,
): never {
  return redirect(
    isSlug(agency) ? outcomePath(kind, agency) : SUBSCRIPTIONS_PATH,
  );
}

export async function createSubscription(
  _previous: FormState,
  formData: FormData,
): Promise<FormState> {
  const values = readValues(formData);
  if (!(await isSubscriptionsEnabled())) {
    return { kind: "gone", values };
  }
  const errors = checkValues(values);
  if (Object.keys(errors).length > 0) {
    return { kind: "invalid", values, errors };
  }
  const result = await callApiAsUser((api) =>
    api.POST("/webhook-subscriptions", {
      body: {
        agency: values.agency,
        url: values.url.trim(),
        format: isFormat(values.format) ? values.format : undefined,
        ...sentTemplate(values),
        ...createdPrompt(formData, values),
      },
    }),
  );
  if (result.status === "ok") {
    return hasSecret(result.data)
      ? {
          kind: "created",
          agency: result.data.agency,
          url: result.data.url,
          secret: result.data.secret,
        }
      : { kind: "unavailable", values };
  }
  return formFailure(result, values);
}

export async function updateSubscription(
  _previous: FormState,
  formData: FormData,
): Promise<FormState> {
  const values = readValues(formData);
  const id = readId(formData);
  if (!(await isSubscriptionsEnabled()) || !isSubscriptionId(id)) {
    return { kind: "gone", values };
  }
  const urlError = checkUrl(values.url);
  const formatError = checkFormat(values.format);
  const templateError = checkTemplate(values);
  const promptError = checkPrompt(values.prompt);
  if (
    urlError !== undefined ||
    formatError !== undefined ||
    templateError !== undefined ||
    promptError !== undefined
  ) {
    return {
      kind: "invalid",
      values,
      errors: {
        ...(urlError === undefined ? {} : { url: urlError }),
        ...(formatError === undefined ? {} : { format: formatError }),
        ...(templateError === undefined ? {} : { template: templateError }),
        ...(promptError === undefined ? {} : { prompt: promptError }),
      },
    };
  }
  const result = await callApiAsUser((api) =>
    api.PATCH("/webhook-subscriptions/{id}", {
      params: { path: { id } },
      body: {
        url: values.url.trim(),
        format: isFormat(values.format) ? values.format : undefined,
        ...sentTemplate(values),
        ...updatedPrompt(formData, values),
      },
    }),
  );
  if (result.status === "ok") {
    return doneAt("updated", result.data.agency);
  }
  return formFailure(result, values);
}

async function setStatus(
  formData: FormData,
  status: "active" | "paused",
): Promise<ActionState> {
  const id = readId(formData);
  if (!(await isSubscriptionsEnabled()) || !isSubscriptionId(id)) {
    return { kind: "gone" };
  }
  const result = await callApiAsUser((api) =>
    api.PATCH("/webhook-subscriptions/{id}", {
      params: { path: { id } },
      body: { status },
    }),
  );
  if (result.status === "ok") {
    const kind = status === "paused" ? "paused" : "resumed";
    const { agency } = result.data;
    if (readReturnsToSubscription(formData) && isSlug(agency)) {
      return redirect(subscriptionOutcomePath(id, kind, agency));
    }
    return doneAt(kind, agency);
  }
  return actionFailure(result);
}

export async function pauseSubscription(
  _previous: ActionState,
  formData: FormData,
): Promise<ActionState> {
  return setStatus(formData, "paused");
}

export async function resumeSubscription(
  _previous: ActionState,
  formData: FormData,
): Promise<ActionState> {
  return setStatus(formData, "active");
}

export async function deleteSubscription(
  _previous: ActionState,
  formData: FormData,
): Promise<ActionState> {
  const id = readId(formData);
  const { agency } = readValues(formData);
  if (!(await isSubscriptionsEnabled()) || !isSubscriptionId(id)) {
    return { kind: "gone" };
  }
  const result = await callApiAsUser(async (api) => {
    const answer = await api.DELETE("/webhook-subscriptions/{id}", {
      params: { path: { id } },
    });
    return { ...answer, data: answer.response.ok ? true : undefined };
  });
  if (result.status === "ok") {
    return doneAt("deleted", agency);
  }
  return actionFailure(result);
}

export async function rotateSecret(
  _previous: ActionState,
  formData: FormData,
): Promise<ActionState> {
  const id = readId(formData);
  if (!(await isSubscriptionsEnabled()) || !isSubscriptionId(id)) {
    return { kind: "gone" };
  }
  const result = await callApiAsUser((api) =>
    api.POST("/webhook-subscriptions/{id}/secret", {
      params: { path: { id } },
    }),
  );
  if (result.status === "ok") {
    return hasSecret(result.data)
      ? { kind: "rotated", secret: result.data.secret }
      : { kind: "unavailable" };
  }
  return actionFailure(result);
}

function testFailure(
  result: UserApiResult<unknown>,
  retryAfter: string | null,
): TestState {
  switch (result.status) {
    case "signed-out":
      return redirect(SUBSCRIPTIONS_SIGN_IN_PATH);
    case "not-found":
      return { kind: "gone" };
    case "payment-required":
      return { kind: "upgrade" };
    case "rejected":
      if (result.httpStatus === 429) {
        return { kind: "limited", retryAfter: readRetryAfter(retryAfter) };
      }
      return { kind: "unavailable" };
    default:
      return { kind: "unavailable" };
  }
}

export async function sendTest(
  _previous: TestState,
  formData: FormData,
): Promise<TestState> {
  const id = readId(formData);
  if (!(await isSubscriptionsEnabled()) || !isSubscriptionId(id)) {
    return { kind: "gone" };
  }
  const chosen = formData.get("type");
  const type = isEventType(chosen) ? chosen : DEFAULT_EVENT_TYPE;
  const seen = { retryAfter: null as string | null };
  const result = await callApiAsUser(async (api) => {
    const answer = await api.POST("/webhook-subscriptions/{id}/test", {
      params: { path: { id } },
      body: { type },
    });
    seen.retryAfter = answer.response.headers.get("retry-after");
    return answer;
  });
  if (result.status === "ok") {
    return { kind: "sent", result: result.data };
  }
  return testFailure(result, seen.retryAfter);
}

export interface PreviewInput {
  template: string;
  type: string;
  agency: string;
}

export async function previewTemplate(
  input: PreviewInput,
): Promise<PreviewState> {
  const template = typeof input.template === "string" ? input.template : "";
  const local = localProblem(template);
  if (local !== undefined) {
    return { kind: "invalid", problem: local };
  }
  if (!(await isSubscriptionsEnabled())) {
    return { kind: "unavailable" };
  }
  const type = isEventType(input.type) ? input.type : DEFAULT_EVENT_TYPE;
  const agency =
    typeof input.agency === "string" && isSlug(input.agency)
      ? { agency: input.agency }
      : {};
  const result = await callApiAsUser((api) =>
    api.POST("/webhook-templates/preview", {
      body: { template, type, ...agency },
    }),
  );
  if (result.status === "ok") {
    return { kind: "rendered", type: result.data.type, body: result.data.body };
  }
  if (result.status === "rejected") {
    const { code, message } = (result.error ?? {}) as {
      code?: unknown;
      message?: unknown;
    };
    const problem = templateProblem(code, message, template);
    if (problem !== undefined) {
      return { kind: "invalid", problem };
    }
  }
  return { kind: "unavailable" };
}
