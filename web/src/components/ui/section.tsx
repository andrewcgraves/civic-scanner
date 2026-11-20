import { useId, type ReactNode } from "react";

export function Section({
  title,
  description,
  actions,
  level = 2,
  id,
  children,
}: {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  level?: 2 | 3;
  id?: string;
  children: ReactNode;
}) {
  const headingId = useId();
  const Heading = level === 2 ? "h2" : "h3";
  return (
    <section id={id} aria-labelledby={headingId} className="mt-12 first:mt-0">
      <div className="mb-4 flex flex-wrap items-baseline justify-between gap-x-4 gap-y-2">
        <Heading
          id={headingId}
          className={
            level === 2
              ? "text-xl font-semibold tracking-tight sm:text-2xl"
              : "text-lg font-semibold"
          }
        >
          {title}
        </Heading>
        {actions === undefined ? null : (
          <div className="text-sm">{actions}</div>
        )}
      </div>
      {description === undefined ? null : (
        <p className="mb-4 max-w-[65ch] text-ink-muted">{description}</p>
      )}
      {children}
    </section>
  );
}
