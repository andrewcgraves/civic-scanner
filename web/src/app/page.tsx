import Link from "next/link";

export default function HomePage() {
  return (
    <>
      <h1 className="text-2xl font-semibold">Civic Scanner</h1>
      <p className="mt-2">
        Track public agency meetings, agendas, and transcripts.
      </p>
      <p className="mt-4">
        <Link href="/agencies" className="underline">
          Browse agencies
        </Link>
      </p>
    </>
  );
}
