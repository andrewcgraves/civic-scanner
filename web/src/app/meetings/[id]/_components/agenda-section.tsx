import { AiSummary } from "@/components/ui/ai-summary";
import { MeetingTime } from "@/components/ui/date-time";
import { EmptyState } from "@/components/ui/empty-state";
import { ExternalLink, isWebUrl } from "@/components/ui/external-link";
import { Notice } from "@/components/ui/notice";
import { Section } from "@/components/ui/section";
import type { Agenda, MeetingStatus } from "@/lib/api/client";

export const AGENDA_SECTION_ID = "agenda";

function OriginalAgendaLink({
  agenda,
  agencyName,
}: {
  agenda: Agenda;
  agencyName: string;
}) {
  return (
    <div className="rounded-lg border border-line-strong bg-surface px-5 py-4">
      {isWebUrl(agenda.documentUrl) ? (
        <p className="text-lg font-medium">
          <ExternalLink href={agenda.documentUrl}>
            Read the original agenda from {agencyName}
          </ExternalLink>
        </p>
      ) : (
        <p className="font-medium">
          The address {agencyName} gave for the original agenda is not a web
          link, so it cannot be opened from here.
        </p>
      )}
      <p className="mt-1 text-sm text-ink-muted">
        {agenda.version > 1
          ? `Revised agenda, version ${String(agenda.version)}. `
          : null}
        This version was found on <MeetingTime value={agenda.foundAt} />.
      </p>
    </div>
  );
}

function SummaryNote({ agenda }: { agenda: Agenda }) {
  return (
    <>
      Written by AI from the agency&apos;s agenda. It can be wrong or leave
      things out;{" "}
      <ExternalLink href={agenda.documentUrl}>
        the agency&apos;s original agenda
      </ExternalLink>{" "}
      is the official record.
    </>
  );
}

function ChangeNoteNote({ agenda }: { agenda: Agenda }) {
  return (
    <>
      Written automatically, usually by AI, from the differences between this
      version and the one before it. It can be wrong or leave things out;{" "}
      <ExternalLink href={agenda.documentUrl}>
        the agency&apos;s original agenda
      </ExternalLink>{" "}
      is the official record.
    </>
  );
}

function WhatChanged({ agenda }: { agenda: Agenda }) {
  const title = `What changed in version ${String(agenda.version)}`;
  if (agenda.changeNote !== null) {
    return (
      <Section level={3} title={title}>
        <AiSummary
          text={agenda.changeNote}
          note={<ChangeNoteNote agenda={agenda} />}
        />
      </Section>
    );
  }
  if (agenda.version > 1) {
    return (
      <Section level={3} title={title}>
        <Notice>
          The agency revised this agenda. No description of what changed from
          version {String(agenda.version - 1)} is available; the original agenda
          shows the current version in full.
        </Notice>
      </Section>
    );
  }
  return null;
}

function Summary({ agenda }: { agenda: Agenda }) {
  return (
    <Section level={3} title="Summary">
      {agenda.summary === null ? (
        <Notice>
          The AI summary of this agenda has not been written yet. Until it is,
          the original agenda above is the place to read what the meeting
          covers.
        </Notice>
      ) : (
        <AiSummary
          text={agenda.summary}
          note={<SummaryNote agenda={agenda} />}
        />
      )}
    </Section>
  );
}

function NoAgenda({ status }: { status: MeetingStatus }) {
  if (status === "cancelled") {
    return (
      <EmptyState title="No agenda">
        Civic Scanner did not find an agenda for this meeting before it was
        cancelled.
      </EmptyState>
    );
  }
  return (
    <EmptyState title="No agenda yet">
      Civic Scanner has not found an agenda for this meeting. When the agency
      publishes one, a link to it and a summary will appear here.
    </EmptyState>
  );
}

export function AgendaSection({
  agenda,
  agencyName,
  status,
}: {
  agenda: Agenda | null;
  agencyName: string;
  status: MeetingStatus;
}) {
  return (
    <Section id={AGENDA_SECTION_ID} title="Agenda">
      {agenda === null ? (
        <NoAgenda status={status} />
      ) : (
        <div>
          <OriginalAgendaLink agenda={agenda} agencyName={agencyName} />
          <WhatChanged agenda={agenda} />
          <Summary agenda={agenda} />
        </div>
      )}
    </Section>
  );
}
