import Link from "next/link";

import { Badge } from "@/components/ui/badge";

import { DASHBOARD_PATH, SUBSCRIPTIONS_PATH } from "../paths";

const TAB = "-mb-px border-b-2 pb-3";

export type DashboardTab = "overview" | "subscriptions";

function Tab({
  href,
  label,
  current,
}: {
  href: string;
  label: string;
  current: boolean;
}) {
  return (
    <li className={`${TAB} ${current ? "border-ink" : "border-transparent"}`}>
      <Link
        href={href}
        aria-current={current ? "page" : undefined}
        className={
          current
            ? "text-ink"
            : "text-ink-muted hover:text-ink hover:underline hover:underline-offset-4"
        }
      >
        {label}
      </Link>
    </li>
  );
}

export function DashboardNav({
  billing,
  subscriptions = false,
  current = "overview",
}: {
  billing: boolean;
  subscriptions?: boolean;
  current?: DashboardTab;
}) {
  return (
    <nav aria-label="Dashboard" className="mb-10 border-b border-line">
      <ul className="flex flex-wrap items-center gap-x-6 gap-y-2 text-sm font-medium sm:gap-x-7">
        <Tab
          href={DASHBOARD_PATH}
          label="Overview"
          current={current === "overview"}
        />
        {subscriptions ? (
          <Tab
            href={SUBSCRIPTIONS_PATH}
            label="Subscriptions"
            current={current === "subscriptions"}
          />
        ) : (
          <li
            className={`${TAB} flex items-center gap-2 border-transparent text-ink-muted`}
          >
            Subscriptions <Badge>Soon</Badge>
          </li>
        )}
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
