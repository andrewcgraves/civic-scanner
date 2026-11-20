"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

export const NAV_ITEMS = [{ href: "/agencies", label: "Agencies" }] as const;

function isCurrent(pathname: string, href: string): boolean {
  return pathname === href || pathname.startsWith(`${href}/`);
}

export function SiteNav() {
  const pathname = usePathname();
  return (
    <nav aria-label="Primary">
      <ul className="flex items-center gap-5 text-sm font-medium">
        {NAV_ITEMS.map((item) => {
          const current = isCurrent(pathname, item.href);
          return (
            <li key={item.href}>
              <Link
                href={item.href}
                aria-current={current ? "page" : undefined}
                className={
                  current
                    ? "text-ink underline decoration-2 underline-offset-8"
                    : "text-ink-muted hover:text-ink hover:underline hover:underline-offset-8"
                }
              >
                {item.label}
              </Link>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}
