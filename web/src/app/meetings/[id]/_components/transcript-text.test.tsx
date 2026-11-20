import { render, screen, within } from "@testing-library/react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import type { TranscriptSegment } from "@/lib/api/client";

import { TRANSCRIPT_EMPTY, TranscriptText } from "./transcript-text";

function seg(startMs: number, text: string): TranscriptSegment {
  return { startMs, endMs: startMs + 4000, text };
}

function longTranscript(count: number): TranscriptSegment[] {
  return Array.from({ length: count }, (_, index) =>
    seg(
      index * 5000,
      `Segment ${index} where the board discusses the item on the agenda in some detail before moving on.`,
    ),
  );
}

describe("TranscriptText", () => {
  it("shows each block with a timestamp that links to its own anchor", () => {
    const { container } = render(
      <TranscriptText
        segments={[seg(0, "Good morning."), seg(3_723_000, "Adjourned.")]}
        anchorPrefix="transcript-1"
        recordingLabel="the recording"
      />,
    );

    const block = container.querySelector("#transcript-1-3723");
    expect(block).not.toBeNull();
    expect(block).toHaveTextContent("Adjourned.");
    const link = within(block as HTMLElement).getByRole("link", {
      name: /^Link to\s*1:02:03$/,
    });
    expect(link).toHaveAttribute("href", "#transcript-1-3723");
    expect(block?.querySelector("time")).toHaveAttribute(
      "dateTime",
      "PT1H2M3S",
    );
    expect(screen.getByText("Good morning.")).toBeVisible();
  });

  it("says the transcript has no text when no segment has any", () => {
    render(
      <TranscriptText
        segments={[seg(0, "  ")]}
        anchorPrefix="transcript-1"
        recordingLabel="the recording"
      />,
    );

    expect(screen.getByText(TRANSCRIPT_EMPTY)).toBeVisible();
    expect(screen.queryByRole("navigation")).toBeNull();
  });

  it("puts text without a usable time last, marked as such", () => {
    const { container } = render(
      <TranscriptText
        segments={[seg(-1000, "Lost."), seg(1000, "Found.")]}
        anchorPrefix="transcript-1"
        recordingLabel="the recording"
      />,
    );

    const untimed = container.querySelector("#transcript-1-untimed");
    expect(untimed).toHaveTextContent("No usable time");
    expect(untimed).toHaveTextContent("Lost.");
    expect(container.querySelector("#transcript-1-1")).toHaveTextContent(
      "Found.",
    );
  });

  it("renders only untimed text when no segment has a usable time", () => {
    const { container } = render(
      <TranscriptText
        segments={[seg(Number.POSITIVE_INFINITY, "Somewhere.")]}
        anchorPrefix="transcript-1"
        recordingLabel="the recording"
      />,
    );

    expect(container.querySelector("#transcript-1-untimed")).toHaveTextContent(
      "Somewhere.",
    );
  });

  it("lays out a four-hour transcript as minute blocks with quarter-hour jumps", () => {
    const segments = longTranscript(3000);

    const { container } = render(
      <TranscriptText
        segments={segments}
        anchorPrefix="transcript-1"
        recordingLabel="the recording"
      />,
    );

    const blocks = container.querySelectorAll('[id^="transcript-1-"]');
    expect(blocks).toHaveLength(250);
    const ids = Array.from(blocks, (block) => block.id);
    expect(new Set(ids).size).toBe(ids.length);
    expect(container.querySelectorAll("p")).toHaveLength(250 + 1);

    const nav = screen.getByRole("navigation", {
      name: "Jump to a time in the recording",
    });
    const jumps = within(nav).getAllByRole("link");
    expect(jumps).toHaveLength(17);
    expect(jumps[1]).toHaveTextContent("15:00");
    expect(jumps[16]).toHaveTextContent("4:00:00");
    for (const jump of jumps) {
      const target = (jump.getAttribute("href") ?? "").slice(1);
      expect(container.querySelector(`[id="${target}"]`)).not.toBeNull();
    }

    expect(container).toHaveTextContent("Segment 2999 where");
  });

  it("keeps a 3,000-segment transcript's markup close to the size of its text", () => {
    const segments = longTranscript(3000);
    const textBytes = segments.reduce(
      (total, segment) => total + segment.text.length + 1,
      0,
    );

    const html = renderToStaticMarkup(
      <TranscriptText
        segments={segments}
        anchorPrefix="transcript-1"
        recordingLabel="the recording"
      />,
    );

    expect(html.length).toBeLessThan(textBytes * 1.3);
  });
});
