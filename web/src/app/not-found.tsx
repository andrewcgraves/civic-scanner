import Link from "next/link";

import { PageHeader } from "@/components/ui/page-header";

export default function NotFound() {
  return (
    <>
      <PageHeader
        title="Page not found"
        description="There is nothing at this address. It may have moved, or the link may be wrong."
      />
      <p>
        <Link href="/" className="link">
          Go to the home page
        </Link>{" "}
        or{" "}
        <Link href="/agencies" className="link">
          browse agencies
        </Link>
        .
      </p>
    </>
  );
}
