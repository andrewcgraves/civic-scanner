import { describe, expect, it } from "vitest";

import type { TranscriptSegment } from "@/lib/api/client";

import {
  BLOCK_MS,
  blockId,
  layoutTranscript,
  MAX_OFFSET_MS,
  transcriptAnchorPrefix,
} from "./transcript-blocks";

function seg(startMs: number, text: string): TranscriptSegment {
  return { startMs, endMs: startMs + 5000, text };
}

describe("layoutTranscript", () => {
  it("groups segments into one-minute blocks named by their first second", () => {
    const layout = layoutTranscript(
      [
        seg(0, "Call to order."),
        seg(20_000, "Roll call."),
        seg(61_500, "Item one."),
      ],
      "transcript-1",
    );

    expect(layout.blocks).toEqual([
      { startMs: 0, id: "transcript-1-0", text: "Call to order. Roll call." },
      { startMs: 61_500, id: "transcript-1-61", text: "Item one." },
    ]);
    expect(layout.untimed).toBeNull();
    expect(layout.jumps).toEqual([]);
  });

  it("starts a block at exactly one minute after the previous block", () => {
    const layout = layoutTranscript(
      [seg(0, "a"), seg(BLOCK_MS - 1, "b"), seg(BLOCK_MS, "c")],
      "p",
    );

    expect(layout.blocks.map((block) => block.text)).toEqual(["a b", "c"]);
  });

  it("sorts segments that arrive out of order", () => {
    const layout = layoutTranscript(
      [seg(90_000, "third"), seg(0, "first"), seg(10_000, "second")],
      "p",
    );

    expect(layout.blocks.map((block) => block.text)).toEqual([
      "first second",
      "third",
    ]);
  });

  it("drops empty text and collapses whitespace", () => {
    const layout = layoutTranscript(
      [
        seg(0, "  "),
        seg(1000, ""),
        { startMs: 2000, endMs: 3000, text: null as unknown as string },
        seg(4000, " Public\n comment  opens. "),
      ],
      "p",
    );

    expect(layout.blocks).toEqual([
      { startMs: 4000, id: "p-4", text: "Public comment opens." },
    ]);
  });

  it("returns nothing for no segments or only empty ones", () => {
    expect(layoutTranscript([], "p")).toEqual({
      blocks: [],
      untimed: null,
      jumps: [],
    });
    expect(layoutTranscript([seg(0, " ")], "p").blocks).toEqual([]);
  });

  it("keeps text with a negative, huge or non-finite start apart, without a time", () => {
    const layout = layoutTranscript(
      [
        seg(-5, "negative"),
        seg(MAX_OFFSET_MS + 1, "huge"),
        seg(Number.NaN, "nan"),
        seg(MAX_OFFSET_MS, "last moment"),
      ],
      "p",
    );

    expect(layout.blocks).toEqual([
      { startMs: MAX_OFFSET_MS, id: "p-86400", text: "last moment" },
    ]);
    expect(layout.untimed).toEqual({
      id: "p-untimed",
      text: "negative huge nan",
    });
  });

  it("offers a jump link for the first block of each quarter hour", () => {
    const segments = Array.from({ length: 40 }, (_, minute) =>
      seg(minute * 60_000 + 1000, `minute ${minute}`),
    );

    const { jumps } = layoutTranscript(segments, "p");

    expect(jumps.map((jump) => jump.id)).toEqual(["p-1", "p-901", "p-1801"]);
  });

  it("skips quarter hours without speech in the jump links", () => {
    const { jumps } = layoutTranscript(
      [seg(0, "a"), seg(50 * 60_000, "b")],
      "p",
    );

    expect(jumps.map((jump) => jump.id)).toEqual(["p-0", "p-3000"]);
  });
});

describe("anchors", () => {
  it("names anchors after the recording and the second", () => {
    expect(transcriptAnchorPrefix(2)).toBe("transcript-2");
    expect(blockId("transcript-2", 3_723_999)).toBe("transcript-2-3723");
  });
});
