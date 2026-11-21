import Link from "next/link";

import { Badge } from "@/components/ui/badge";

import { DASHBOARD_PATH } from "../paths";

const TAB = "-mb-px border-b-2 pb-3";

export function DashboardNav({ billing }: { billing: boolean }) {
  return (
    <nav aria-label="Dashboard" className="mb-10 border-b border-line">
      <ul className="flex flex-wrap items-center gap-x-6 gap-y-2 text-sm font-medium sm:gap-x-7">
        <li className={`${TAB} border-ink`}>
          <Link href={DASHBOARD_PATH} aria-current="page" className="text-ink">
            Overview
          </Link>
        </li>
        <li
          className={`${TAB} flex items-center gap-2 border-transparent text-ink-muted`}
        >
          Subscriptions <Badge>Soon</Badge>
        </li>
        {billing ? (
          <li className={`${TAB} border-transparent`}>
            <Link
              href="/billing"
              className="text-ink-muted hover:text-ink hover:underline hover:underline-offset-4"
            >
              Billing
            </Link>
          </li>
        ) : null}
      </ul>
    </nav>
  );
}
