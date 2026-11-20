import type { ReactNode } from "react";

import { Breadcrumb, type BreadcrumbItem } from "./breadcrumb";

export function PageHeader({
  title,
  eyebrow,
  description,
  breadcrumb,
  children,
}: {
  title: ReactNode;
  eyebrow?: ReactNode;
  description?: ReactNode;
  breadcrumb?: readonly BreadcrumbItem[];
  children?: ReactNode;
}) {
  return (
    <header className="mb-10 space-y-3">
      {breadcrumb === undefined ? null : <Breadcrumb items={breadcrumb} />}
      {eyebrow === undefined ? null : (
        <p className="text-sm font-medium tracking-wide text-ink-muted uppercase">
          {eyebrow}
        </p>
      )}
      <h1 className="text-3xl font-semibold tracking-tight text-balance sm:text-4xl">
        {title}
      </h1>
      {description === undefined ? null : (
        <p className="max-w-[65ch] text-lg text-ink-muted">{description}</p>
      )}
      {children === undefined ? null : (
        <div className="flex flex-wrap items-center gap-x-4 gap-y-2 pt-1 text-ink-muted">
          {children}
        </div>
      )}
    </header>
  );
}
