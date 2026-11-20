"use server";

import { redirect } from "next/navigation";

import { callApiAsUser } from "@/lib/auth/api";
import { isSignInEnabled } from "@/lib/auth/server";
import { isFlagEnabled } from "@/lib/flags/server";

import {
  BODY_ERROR,
  SIGN_IN_PATH,
  checkValues,
  fieldErrorsFrom,
  readValues,
  type SubmissionState,
  type SubmissionValues,
} from "./state";

export async function submitAgency(
  _previous: SubmissionState,
  formData: FormData,
): Promise<SubmissionState> {
  const values = readValues(formData);
  const [open, signIn] = await Promise.all([
    isFlagEnabled("agency-submissions"),
    isSignInEnabled(),
  ]);
  if (!open || !signIn) {
    return { kind: "closed", values };
  }

  const errors = checkValues(values);
  if (Object.keys(errors).length > 0) {
    return { kind: "invalid", values, errors };
  }

  const result = await callApiAsUser((api) =>
    api.POST("/agency-submissions", {
      body: { name: values.name, website: values.website },
    }),
  );
  switch (result.status) {
    case "ok":
      return typeof result.data.name === "string" &&
        typeof result.data.website === "string"
        ? {
            kind: "submitted",
            name: result.data.name,
            website: result.data.website,
          }
        : { kind: "unavailable", values };
    case "signed-out":
      return redirect(SIGN_IN_PATH);
    case "not-found":
      return { kind: "closed", values };
    case "conflict":
      return { kind: "on-file", values };
    case "rejected":
      if (result.httpStatus === 429) {
        return { kind: "limit", values };
      }
      return rejected(values, result.error);
    default:
      return { kind: "unavailable", values };
  }
}

function rejected(values: SubmissionValues, body: unknown): SubmissionState {
  const errors = fieldErrorsFrom(body);
  return Object.keys(errors).length > 0
    ? { kind: "invalid", values, errors }
    : { kind: "invalid", values, errors, message: BODY_ERROR };
}
