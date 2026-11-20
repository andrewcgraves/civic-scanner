import type { ReactNode } from "react";

export const EXTERNAL_LINK_NOTE = "(leaves Civic Scanner)";

export function isWebUrl(href: string): boolean {
  try {
    const { protocol } = new URL(href);
    return protocol === "https:" || protocol === "http:";
  } catch {
    return false;
  }
}

export function ExternalLink({
  href,
  children,
}: {
  href: string;
  children: ReactNode;
}) {
  if (!isWebUrl(href)) {
    return <span>{children}</span>;
  }
  return (
    <a href={href} rel="external noopener noreferrer" className="link">
      {children} <span aria-hidden="true">{"↗"}</span>
      <span className="sr-only">{EXTERNAL_LINK_NOTE}</span>
    </a>
  );
}
