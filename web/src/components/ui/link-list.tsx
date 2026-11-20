import Link from "next/link";
import type { ReactNode } from "react";

const ROW = "block px-4 py-4 sm:px-5";

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

function RowContent({
  title,
  titleClassName,
  meta,
  aside,
  description,
}: {
  title: ReactNode;
  titleClassName: string;
  meta?: ReactNode;
  aside?: ReactNode;
  description?: ReactNode;
}) {
  return (
    <>
      <span className="flex items-start justify-between gap-4">
        <span className={`min-w-0 break-words ${titleClassName}`}>{title}</span>
        {aside === undefined ? null : <span className="shrink-0">{aside}</span>}
      </span>
      {meta === undefined ? null : (
        <span className="mt-1 block text-sm text-ink-muted">{meta}</span>
      )}
      {description === undefined ? null : (
        <span className="mt-2 block text-sm">{description}</span>
      )}
    </>
  );
}

export function LinkListItem({
  href,
  title,
  meta,
  aside,
  description,
}: {
  href?: string;
  title: ReactNode;
  meta?: ReactNode;
  aside?: ReactNode;
  description?: ReactNode;
}) {
  if (href === undefined) {
    return (
      <li className={ROW}>
        <RowContent
          title={title}
          titleClassName="font-medium"
          meta={meta}
          aside={aside}
          description={description}
        />
      </li>
    );
  }
  return (
    <li>
      <Link
        href={href}
        className={`group ${ROW} hover:bg-surface-muted focus-visible:-outline-offset-2`}
      >
        <RowContent
          title={title}
          titleClassName="font-medium text-accent group-hover:underline"
          meta={meta}
          aside={aside}
          description={description}
        />
      </Link>
    </li>
  );
}
