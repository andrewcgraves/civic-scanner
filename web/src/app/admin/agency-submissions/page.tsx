import type { Metadata } from "next";
import { cookies } from "next/headers";
import { notFound, redirect } from "next/navigation";
import { connection } from "next/server";

import { EmptyState } from "@/components/ui/empty-state";
import { Notice, UnavailableNotice } from "@/components/ui/notice";
import { PageHeader } from "@/components/ui/page-header";
import { readApiAsUser } from "@/lib/auth/api";
import { sessionCookieName } from "@/lib/auth/cookie";
import { isSignInEnabled } from "@/lib/auth/server";

import { ReviewItem } from "./_components/review-item";
import { REVIEW_SIGN_IN_PATH, readOutcome, type ReviewOutcome } from "./state";

export const metadata: Metadata = {
  title: "Review agencies",
  robots: { index: false },
};

function OutcomeNotice({ outcome }: { outcome: ReviewOutcome }) {
  return (
    <div role="status" className="mb-8">
      {outcome.kind === "approved" ? (
        <Notice title="The agency is live">
          It is listed on the site at{" "}
          <a
            href={`/agencies/${encodeURIComponent(outcome.slug)}`}
            className="link"
          >
            /agencies/{outcome.slug}
          </a>
          . Its meetings appear once a source adapter is registered for this
          slug.
        </Notice>
      ) : (
        <Notice title="The agency was rejected">
          It stays hidden, and its website cannot be suggested again.
        </Notice>
      )}
    </div>
  );
}

export default async function ReviewAgenciesPage({
  searchParams,
}: {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  await connection();
  if (!(await isSignInEnabled())) {
    notFound();
  }
  if (!(await cookies()).has(sessionCookieName())) {
    redirect(REVIEW_SIGN_IN_PATH);
  }
  const result = await readApiAsUser((api) =>
    api.GET("/admin/agency-submissions"),
  );
  if (result.status === "signed-out") {
    redirect(REVIEW_SIGN_IN_PATH);
  }
  if (result.status === "forbidden" || result.status === "not-found") {
    notFound();
  }
  const outcome = readOutcome(await searchParams);

  return (
    <>
      <PageHeader
        title="Review agencies"
        description="Agencies people have suggested, oldest first. Approving one makes it live on the site; rejecting one keeps it hidden. Neither can be undone here."
      />
      {outcome === null ? null : <OutcomeNotice outcome={outcome} />}
      {result.status !== "ok" ? (
        <UnavailableNotice what="Suggested agencies are" />
      ) : result.data.submissions.length === 0 ? (
        <EmptyState title="Nothing to review">
          No suggested agencies are waiting for review.
        </EmptyState>
      ) : (
        <ul className="space-y-6">
          {result.data.submissions.map((submission) => (
            <li key={submission.id}>
              <ReviewItem submission={submission} />
            </li>
          ))}
        </ul>
      )}
    </>
  );
}
