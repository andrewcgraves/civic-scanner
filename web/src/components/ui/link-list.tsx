import Link from "next/link";
import type { ReactNode } from "react";

export function LinkList({
  label,
  children,
}: {
  label?: string;
  children: ReactNode;
}) {
  return (
    <ul
      aria-label={label}
      className="divide-y divide-line overflow-hidden rounded-lg border border-line bg-surface"
    >
      {children}
    </ul>
  );
}

export function LinkListItem({
  href,
  title,
  meta,
  aside,
  description,
}: {
  href: string;
  title: ReactNode;
  meta?: ReactNode;
  aside?: ReactNode;
  description?: ReactNode;
}) {
  return (
    <li>
      <Link
        href={href}
        className="group block px-4 py-4 hover:bg-surface-muted focus-visible:-outline-offset-2 sm:px-5"
      >
        <span className="flex items-start justify-between gap-4">
          <span className="font-medium text-accent group-hover:underline">
            {title}
          </span>
          {aside === undefined ? null : (
            <span className="shrink-0">{aside}</span>
          )}
        </span>
        {meta === undefined ? null : (
          <span className="mt-1 block text-sm text-ink-muted">{meta}</span>
        )}
        {description === undefined ? null : (
          <span className="mt-2 block text-sm">{description}</span>
        )}
      </Link>
    </li>
  );
}
