"use client";

import { PageHeader } from "@/components/ui/page-header";
import { Notice } from "@/components/ui/notice";

export default function ErrorPage({ retry }: { retry: () => void }) {
  return (
    <>
      <PageHeader title="Something went wrong" />
      <Notice tone="error">
        This page could not be shown. Please try again in a moment.
      </Notice>
      <button
        type="button"
        onClick={retry}
        className="mt-6 rounded-md border border-line-strong bg-surface px-4 py-2 font-medium text-accent hover:bg-surface-muted"
      >
        Try again
      </button>
    </>
  );
}
