import type { ReactNode } from "react";

import { AiSummary } from "@/components/ui/ai-summary";
import { EmptyState } from "@/components/ui/empty-state";
import { ExternalLink, isWebUrl } from "@/components/ui/external-link";
import { Notice } from "@/components/ui/notice";
import { Section } from "@/components/ui/section";
import type { MeetingDetail, MeetingStatus, Recording } from "@/lib/api/client";
import { isFlagEnabled } from "@/lib/flags/server";

import { transcriptAnchorPrefix } from "./transcript-blocks";
import { TranscriptText } from "./transcript-text";

export const TRANSCRIPT_SECTION_ID = "transcript";

export const COPY = {
  scheduledTitle: "No recording yet",
  scheduledBody:
    "If the agency posts a recording of this meeting and Civic Scanner finds it, a link to it will appear here, followed by a transcript and a summary.",
  heldTitle: "No recording available yet",
  heldBody:
    "Civic Scanner has not found a recording of this meeting. Agencies post recordings after a meeting, if at all. If one is found, a link to it will appear here, followed by a transcript and a summary.",
  recordingLink: "Watch or listen on the agency's site",
  recordingNotWeb:
    "The agency gave an address for this recording that is not a web link, so it cannot be opened from here.",
  transcriptNotReady:
    "The transcript is not ready yet. It is made from the recording after the recording is found, which can take a while.",
  summaryNotReady:
    "The summary is not ready yet. It is written from the transcript below.",
  summaryNote:
    "Written by AI from the machine-made transcript below. It can be wrong or leave things out; the recording is the record of what was said.",
  transcriptNote:
    "Made automatically from the recording's audio. It can mishear words, especially names and numbers, and it does not say who is speaking. Times are from the start of the recording. The recording is the authority on what was said.",
} as const;

function SubHeading({ level, children }: { level: 3 | 4; children: string }) {
  const Heading = level === 3 ? "h3" : "h4";
  return (
    <Heading className="mt-8 mb-3 text-lg font-semibold">{children}</Heading>
  );
}

function RecordingLink({ href }: { href: string }) {
  if (!isWebUrl(href)) {
    return <p className="text-ink-muted">{COPY.recordingNotWeb}</p>;
  }
  return (
    <p>
      <ExternalLink href={href}>{COPY.recordingLink}</ExternalLink>
    </p>
  );
}

function RecordingBody({
  recording,
  recordingNumber,
  recordingLabel,
  headingLevel,
}: {
  recording: Recording;
  recordingNumber: number;
  recordingLabel: string;
  headingLevel: 3 | 4;
}) {
  const { transcript } = recording;
  return (
    <>
      <RecordingLink href={recording.sourceUrl} />
      {transcript === null ? (
        <div className="mt-4">
          <Notice>{COPY.transcriptNotReady}</Notice>
        </div>
      ) : (
        <>
          <SubHeading level={headingLevel}>Summary</SubHeading>
          {transcript.summary === null || transcript.summary.trim() === "" ? (
            <Notice>{COPY.summaryNotReady}</Notice>
          ) : (
            <AiSummary text={transcript.summary} note={COPY.summaryNote} />
          )}
          <SubHeading level={headingLevel}>Transcript</SubHeading>
          <p className="mb-4 max-w-[65ch] text-sm text-ink-muted">
            {COPY.transcriptNote}
          </p>
          <TranscriptText
            segments={transcript.segments}
            anchorPrefix={transcriptAnchorPrefix(recordingNumber)}
            recordingLabel={recordingLabel}
          />
        </>
      )}
    </>
  );
}

function recordingId(recordingNumber: number): string {
  return `recording-${recordingNumber}`;
}

function SeveralRecordings({ recordings }: { recordings: Recording[] }) {
  const total = recordings.length;
  return (
    <>
      <nav aria-label="Recordings of this meeting">
        <p>This meeting has {total} recordings.</p>
        <ul className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-sm">
          {recordings.map((_recording, index) => (
            <li key={index}>
              <a href={`#${recordingId(index + 1)}`} className="link">
                Recording {index + 1}
              </a>
            </li>
          ))}
        </ul>
      </nav>
      {recordings.map((recording, index) => {
        const label = `Recording ${index + 1} of ${total}`;
        return (
          <div key={index} className="mt-10 border-t-2 border-line-strong pt-6">
            <Section level={3} id={recordingId(index + 1)} title={label}>
              <RecordingBody
                recording={recording}
                recordingNumber={index + 1}
                recordingLabel={`recording ${index + 1}`}
                headingLevel={4}
              />
            </Section>
          </div>
        );
      })}
    </>
  );
}

function noRecording(status: MeetingStatus): ReactNode {
  if (status === "cancelled") {
    return null;
  }
  if (status === "scheduled") {
    return (
      <EmptyState title={COPY.scheduledTitle}>{COPY.scheduledBody}</EmptyState>
    );
  }
  return <EmptyState title={COPY.heldTitle}>{COPY.heldBody}</EmptyState>;
}

function content(meeting: MeetingDetail): ReactNode {
  const [first, ...others] = meeting.recordings;
  if (first === undefined) {
    return noRecording(meeting.status);
  }
  if (others.length === 0) {
    return (
      <RecordingBody
        recording={first}
        recordingNumber={1}
        recordingLabel="the recording"
        headingLevel={3}
      />
    );
  }
  return <SeveralRecordings recordings={meeting.recordings} />;
}

export async function TranscriptSection({
  meeting,
}: {
  meeting: MeetingDetail;
}) {
  if (!(await isFlagEnabled("meeting-transcripts"))) {
    return null;
  }
  const body = content(meeting);
  if (body === null) {
    return null;
  }
  return (
    <Section
      id={TRANSCRIPT_SECTION_ID}
      title={
        meeting.recordings.length > 1
          ? "Recordings and transcripts"
          : "Recording and transcript"
      }
    >
      {body}
    </Section>
  );
}
