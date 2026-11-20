import { Offset } from "@/components/ui/date-time";
import { Notice } from "@/components/ui/notice";
import type { TranscriptSegment } from "@/lib/api/client";

import { layoutTranscript } from "./transcript-blocks";

export const TRANSCRIPT_EMPTY =
  "This transcript has no text. The recording may have no speech, or the transcription found none.";

export function TranscriptText({
  segments,
  anchorPrefix,
  recordingLabel,
}: {
  segments: readonly TranscriptSegment[];
  anchorPrefix: string;
  recordingLabel: string;
}) {
  const { blocks, untimed, jumps } = layoutTranscript(segments, anchorPrefix);

  if (blocks.length === 0 && untimed === null) {
    return <Notice>{TRANSCRIPT_EMPTY}</Notice>;
  }

  return (
    <div>
      {jumps.length === 0 ? null : (
        <nav
          aria-label={`Jump to a time in ${recordingLabel}`}
          className="mb-4 text-sm"
        >
          <p className="mb-1 text-ink-muted">Jump to</p>
          <ul className="flex flex-wrap gap-x-4 gap-y-1">
            {jumps.map((jump) => (
              <li key={jump.id}>
                <a href={`#${jump.id}`} className="link">
                  <Offset ms={jump.startMs} />
                </a>
              </li>
            ))}
          </ul>
        </nav>
      )}
      <div className="divide-y divide-line border-y border-line">
        {blocks.map((block) => (
          <div
            key={block.id}
            id={block.id}
            className="grid gap-x-4 gap-y-1 py-3 sm:grid-cols-[5.5rem_1fr]"
          >
            <a
              href={`#${block.id}`}
              className="link self-start text-sm text-ink-muted"
            >
              <span className="sr-only">Link to </span>
              <Offset ms={block.startMs} />
            </a>
            <p className="max-w-[70ch] leading-7">{block.text}</p>
          </div>
        ))}
        {untimed === null ? null : (
          <div
            id={untimed.id}
            className="grid gap-x-4 gap-y-1 py-3 sm:grid-cols-[5.5rem_1fr]"
          >
            <p className="text-sm text-ink-muted">No usable time</p>
            <p className="max-w-[70ch] leading-7">{untimed.text}</p>
          </div>
        )}
      </div>
    </div>
  );
}
