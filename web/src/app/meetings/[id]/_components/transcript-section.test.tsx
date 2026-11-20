import { render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { MeetingDisplayKind } from "@/components/ui/meeting-status-badge";
import type { MeetingDetail, MeetingStatus, Recording } from "@/lib/api/client";

const mocks = vi.hoisted(() => ({ isFlagEnabled: vi.fn() }));

vi.mock("@/lib/flags/server", () => ({ isFlagEnabled: mocks.isFlagEnabled }));

import { COPY, TranscriptSection } from "./transcript-section";
import { TRANSCRIPT_EMPTY } from "./transcript-text";

function meeting(
  status: MeetingStatus,
  recordings: Recording[] = [],
): MeetingDetail {
  return {
    id: "6f1c1d5e-0d6a-4d55-9a53-4c1f2f3c9a10",
    agency: {
      id: "0b0b5a5e-1d1d-4c4c-8a8a-7f7f7f7f7f7f",
      slug: "la-metro",
      name: "LA Metro",
      website: "https://www.metro.net",
    },
    title: "Board of Directors",
    startsAt: "2027-01-20T17:00:00Z",
    status,
    agenda: null,
    recordings,
  };
}

const VIDEO = "https://metro.granicus.com/player/clip/100";

function transcribed(summary: string | null): Recording {
  return {
    sourceUrl: VIDEO,
    transcript: {
      summary,
      segments: [
        { startMs: 0, endMs: 4000, text: "Good morning." },
        { startMs: 75_000, endMs: 80_000, text: "Item one is approved." },
      ],
    },
  };
}

async function show(
  detail: MeetingDetail,
  display: MeetingDisplayKind = detail.status,
) {
  return render(await TranscriptSection({ meeting: detail, display }));
}

describe("TranscriptSection", () => {
  beforeEach(() => {
    mocks.isFlagEnabled.mockReset();
    mocks.isFlagEnabled.mockResolvedValue(true);
  });

  it("renders nothing when the meeting-transcripts flag is off", async () => {
    mocks.isFlagEnabled.mockResolvedValue(false);

    const { container } = await show(meeting("held", [transcribed("S")]));

    expect(container).toBeEmptyDOMElement();
    expect(mocks.isFlagEnabled).toHaveBeenCalledWith("meeting-transcripts");
  });

  it("says a scheduled meeting has no recording yet without promising one", async () => {
    await show(meeting("scheduled"));

    const region = screen.getByRole("region", {
      name: "Recording and transcript",
    });
    expect(region).toHaveAttribute("id", "transcript");
    expect(within(region).getByText(COPY.scheduledTitle)).toBeVisible();
    expect(within(region).getByText(COPY.scheduledBody)).toBeVisible();
    expect(within(region).queryByRole("link")).toBeNull();
  });

  it("says a scheduled meeting whose date has passed has no recording, without calling it upcoming or held", async () => {
    await show(meeting("scheduled"), "date-passed");

    const region = screen.getByRole("region", {
      name: "Recording and transcript",
    });
    expect(within(region).getByText(COPY.datePassedTitle)).toBeVisible();
    expect(within(region).getByText(COPY.datePassedBody)).toBeVisible();
    expect(within(region).queryByText(COPY.scheduledTitle)).toBeNull();
    expect(region).not.toHaveTextContent(/\bheld\b|\bupcoming\b|will be/i);
  });

  it("uses the general wording for a meeting whose status is not known", async () => {
    await show(meeting("scheduled"), "other");

    expect(screen.getByText(COPY.heldTitle)).toBeVisible();
    expect(screen.queryByText(COPY.scheduledTitle)).toBeNull();
  });

  it("says a held meeting's recording is not available yet", async () => {
    await show(meeting("held"));

    expect(screen.getByText(COPY.heldTitle)).toBeVisible();
    expect(screen.getByText(COPY.heldBody)).toBeVisible();
  });

  it("renders nothing for a cancelled meeting without recordings", async () => {
    const { container } = await show(meeting("cancelled"));

    expect(container).toBeEmptyDOMElement();
  });

  it("still shows a recording that a cancelled meeting has", async () => {
    await show(meeting("cancelled", [transcribed(null)]));

    expect(
      screen.getByRole("link", {
        name: /Watch or listen on the agency's site/,
      }),
    ).toHaveAttribute("href", VIDEO);
  });

  it("links to the recording and says the transcript is not ready", async () => {
    await show(meeting("held", [{ sourceUrl: VIDEO, transcript: null }]));

    const link = screen.getByRole("link", {
      name: "Watch or listen on the agency's site (leaves Civic Scanner)",
    });
    expect(link).toHaveAttribute("href", VIDEO);
    expect(screen.getByText(COPY.transcriptNotReady)).toBeVisible();
    expect(screen.queryByRole("heading", { name: "Transcript" })).toBeNull();
    expect(screen.queryByRole("heading", { name: "Summary" })).toBeNull();
  });

  it("shows the transcript and says the summary is not ready", async () => {
    const { container } = await show(meeting("held", [transcribed(null)]));

    expect(
      screen.getByRole("heading", { level: 3, name: "Summary" }),
    ).toBeVisible();
    expect(screen.getByText(COPY.summaryNotReady)).toBeVisible();
    expect(screen.queryByText("AI-generated")).toBeNull();
    expect(
      screen.getByRole("heading", { level: 3, name: "Transcript" }),
    ).toBeVisible();
    expect(screen.getByText(COPY.transcriptNote)).toBeVisible();
    expect(container.querySelector("#transcript-1-75")).toHaveTextContent(
      "Item one is approved.",
    );
  });

  it("treats a blank summary as not ready", async () => {
    await show(meeting("held", [transcribed("  \n ")]));

    expect(screen.getByText(COPY.summaryNotReady)).toBeVisible();
  });

  it("shows the AI summary before the transcript", async () => {
    await show(
      meeting("held", [
        transcribed("The board approved item one.\nNo one spoke."),
      ]),
    );

    expect(screen.getByText("AI-generated")).toBeVisible();
    const summary = screen.getByText("The board approved item one.");
    expect(screen.getByText("No one spoke.")).toBeVisible();
    expect(screen.getByText(COPY.summaryNote)).toBeVisible();
    expect(screen.queryByText(COPY.summaryNotReady)).toBeNull();
    const transcript = screen.getByText("Good morning.");
    expect(
      summary.compareDocumentPosition(transcript) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("says a transcript without segments has no text", async () => {
    await show(
      meeting("held", [
        { sourceUrl: VIDEO, transcript: { summary: null, segments: [] } },
      ]),
    );

    expect(screen.getByText(TRANSCRIPT_EMPTY)).toBeVisible();
  });

  it("does not offer a link for a recording address that is not a web link", async () => {
    await show(
      meeting("held", [{ sourceUrl: "javascript:alert(1)", transcript: null }]),
    );

    expect(screen.queryByRole("link")).toBeNull();
    expect(screen.getByText(COPY.recordingNotWeb)).toBeVisible();
    expect(screen.getByText(COPY.transcriptNotReady)).toBeVisible();
  });

  it("gives each of several recordings its own labelled block", async () => {
    const second = "https://metro.granicus.com/player/clip/101";
    const { container } = await show(
      meeting("held", [
        transcribed("Part one summary."),
        { sourceUrl: second, transcript: null },
      ]),
    );

    expect(
      screen.getByRole("region", { name: "Recordings and transcripts" }),
    ).toBeVisible();
    expect(screen.getByText("This meeting has 2 recordings.")).toBeVisible();
    const nav = screen.getByRole("navigation", {
      name: "Recordings of this meeting",
    });
    expect(
      within(nav)
        .getAllByRole("link")
        .map((link) => link.getAttribute("href")),
    ).toEqual(["#recording-1", "#recording-2"]);

    const first = screen.getByRole("region", { name: "Recording 1 of 2" });
    expect(first).toHaveAttribute("id", "recording-1");
    expect(
      within(first).getByRole("heading", { level: 4, name: "Summary" }),
    ).toBeVisible();
    expect(within(first).getByText("Part one summary.")).toBeVisible();
    expect(
      within(first).getByRole("link", { name: /Watch or listen/ }),
    ).toHaveAttribute("href", VIDEO);
    expect(container.querySelector("#transcript-1-0")).not.toBeNull();

    const next = screen.getByRole("region", { name: "Recording 2 of 2" });
    expect(
      within(next).getByRole("link", { name: /Watch or listen/ }),
    ).toHaveAttribute("href", second);
    expect(within(next).getByText(COPY.transcriptNotReady)).toBeVisible();
  });

  it("keeps anchors apart when two recordings have the same times", async () => {
    const { container } = await show(
      meeting("held", [transcribed(null), transcribed(null)]),
    );

    expect(container.querySelector("#transcript-1-75")).not.toBeNull();
    expect(container.querySelector("#transcript-2-75")).not.toBeNull();
    expect(
      screen.getByRole("navigation", { name: /Recordings of this meeting/ }),
    ).toBeVisible();
  });
});
