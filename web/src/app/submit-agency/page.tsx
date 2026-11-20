import { withAuth } from "@workos-inc/authkit-nextjs";
import type { Metadata } from "next";
import { cookies, headers } from "next/headers";
import { notFound, redirect } from "next/navigation";
import { connection } from "next/server";

import { Notice } from "@/components/ui/notice";
import { PageHeader } from "@/components/ui/page-header";
import { SESSION_STATE_HEADER, sessionCookieName } from "@/lib/auth/cookie";
import { isSignInEnabled } from "@/lib/auth/server";
import { requireFlag } from "@/lib/flags/server";

import { SubmissionForm } from "./_components/submission-form";
import { SIGN_IN_PATH } from "./state";

export const metadata: Metadata = {
  title: "Suggest an agency",
  description: "Suggest a public agency for Civic Scanner to follow.",
  robots: { index: false },
};

async function signedIn(): Promise<"yes" | "no" | "unknown"> {
  if (!(await cookies()).has(sessionCookieName())) {
    return "no";
  }
  const { user } = await withAuth();
  if (user !== null) {
    return "yes";
  }
  return (await headers()).get(SESSION_STATE_HEADER) === "unverified"
    ? "unknown"
    : "no";
}

export default async function SubmitAgencyPage() {
  await connection();
  await requireFlag("agency-submissions");
  if (!(await isSignInEnabled())) {
    notFound();
  }
  const session = await signedIn();
  if (session === "no") {
    redirect(SIGN_IN_PATH);
  }

  return (
    <>
      <PageHeader
        title="Suggest an agency"
        description="Tell us about a public agency whose meetings Civic Scanner should follow. Suggestions are reviewed before an agency appears on the site."
      />
      {session === "unknown" ? (
        <Notice tone="error">
          Your sign-in cannot be checked right now. Please try again shortly.
        </Notice>
      ) : (
        <SubmissionForm />
      )}
    </>
  );
}
