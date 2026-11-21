"use server";

import { redirect } from "next/navigation";

import { callApiAsUser, type UserApiResult } from "@/lib/auth/api";

import { isSubscriptionsEnabled } from "./server";
import {
  SUBSCRIPTIONS_PATH,
  SUBSCRIPTIONS_SIGN_IN_PATH,
  checkFormat,
  checkUrl,
  checkValues,
  isFormat,
  isSlug,
  isSubscriptionId,
  outcomePath,
  readId,
  readValues,
  rejection,
  type ActionState,
  type FormState,
  type SubscriptionValues,
} from "./state";

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
      return { kind: "invalid", values, ...rejection(result.error) };
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
  if (urlError !== undefined || formatError !== undefined) {
    return {
      kind: "invalid",
      values,
      errors: {
        ...(urlError === undefined ? {} : { url: urlError }),
        ...(formatError === undefined ? {} : { format: formatError }),
      },
    };
  }
  const result = await callApiAsUser((api) =>
    api.PATCH("/webhook-subscriptions/{id}", {
      params: { path: { id } },
      body: {
        url: values.url.trim(),
        format: isFormat(values.format) ? values.format : undefined,
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
    return doneAt(
      status === "paused" ? "paused" : "resumed",
      result.data.agency,
    );
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
