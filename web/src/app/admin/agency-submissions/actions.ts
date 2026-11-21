"use server";

import { redirect } from "next/navigation";

import { callApiAsUser, type UserApiResult } from "@/lib/auth/api";
import { isSignInEnabled } from "@/lib/auth/server";

import {
  REJECTED_PATH,
  REVIEW_SIGN_IN_PATH,
  SLUG_RULE,
  approvedPath,
  errorCode,
  errorMessage,
  isLiveSlug,
  isSubmissionId,
  readReview,
  type ReviewState,
} from "./state";

function failure(result: UserApiResult<unknown>, slug: string): ReviewState {
  switch (result.status) {
    case "signed-out":
      return redirect(REVIEW_SIGN_IN_PATH);
    case "not-found":
      return { kind: "gone", slug };
    case "forbidden":
      return { kind: "not-allowed", slug };
    case "conflict":
      return errorCode(result.error) === "slug_taken"
        ? { kind: "slug-taken", slug }
        : { kind: "reviewed", slug };
    case "rejected":
      return result.httpStatus === 400
        ? {
            kind: "invalid-slug",
            slug,
            message: errorMessage(result.error) ?? SLUG_RULE,
          }
        : { kind: "unavailable", slug };
    default:
      return { kind: "unavailable", slug };
  }
}

export async function approveSubmission(
  _previous: ReviewState,
  formData: FormData,
): Promise<ReviewState> {
  const { id, slug } = readReview(formData);
  if (!(await isSignInEnabled())) {
    return { kind: "not-allowed", slug };
  }
  if (!isSubmissionId(id)) {
    return { kind: "gone", slug };
  }
  if (!isLiveSlug(slug)) {
    return { kind: "invalid-slug", slug, message: SLUG_RULE };
  }
  const result = await callApiAsUser((api) =>
    api.POST("/admin/agency-submissions/{id}/approve", {
      params: { path: { id } },
      body: { slug },
    }),
  );
  if (result.status === "ok") {
    return redirect(approvedPath(result.data.slug));
  }
  return failure(result, slug);
}

export async function rejectSubmission(
  _previous: ReviewState,
  formData: FormData,
): Promise<ReviewState> {
  const { id } = readReview(formData);
  if (!(await isSignInEnabled())) {
    return { kind: "not-allowed", slug: "" };
  }
  if (!isSubmissionId(id)) {
    return { kind: "gone", slug: "" };
  }
  const result = await callApiAsUser((api) =>
    api.POST("/admin/agency-submissions/{id}/reject", {
      params: { path: { id } },
    }),
  );
  if (result.status === "ok") {
    return redirect(REJECTED_PATH);
  }
  return failure(result, "");
}
