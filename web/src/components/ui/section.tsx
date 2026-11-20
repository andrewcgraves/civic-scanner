import { useId, type ReactNode } from "react";

const HEADINGS = {
  2: {
    tag: "h2",
    className: "text-xl font-semibold tracking-tight sm:text-2xl",
    spacing: "mt-12",
  },
  3: { tag: "h3", className: "text-lg font-semibold", spacing: "mt-8" },
  4: { tag: "h4", className: "text-base font-semibold", spacing: "mt-8" },
} as const;

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
  level?: keyof typeof HEADINGS;
  id?: string;
  children: ReactNode;
}) {
  const headingId = useId();
  const { tag: Heading, className, spacing } = HEADINGS[level];
  return (
    <section
      id={id}
      aria-labelledby={headingId}
      className={`${spacing} first:mt-0`}
    >
      <div className="mb-4 flex flex-wrap items-baseline justify-between gap-x-4 gap-y-2">
        <Heading id={headingId} className={className}>
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
