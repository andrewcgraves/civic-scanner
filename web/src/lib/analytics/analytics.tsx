"use client";

import { usePathname, useSearchParams } from "next/navigation";
import { useReportWebVitals } from "next/web-vitals";
import { Suspense, useEffect } from "react";

import type { PostHogProject } from "@/lib/posthog/config";

import { reportWebVital, startAnalytics, trackPageview } from "./client";

function Pageviews({ project }: { project: PostHogProject }) {
  const pathname = usePathname();
  const search = useSearchParams().toString();

  useEffect(() => {
    startAnalytics(project);
    trackPageview(pathname, search);
  }, [project, pathname, search]);

  return null;
}

export function Analytics({ project }: { project: PostHogProject }) {
  useEffect(() => {
    startAnalytics(project);
  }, [project]);
  useReportWebVitals(reportWebVital);

  return (
    <Suspense fallback={null}>
      <Pageviews project={project} />
    </Suspense>
  );
}
