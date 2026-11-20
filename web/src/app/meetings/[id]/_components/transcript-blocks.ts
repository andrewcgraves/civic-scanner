import type { TranscriptSegment } from "@/lib/api/client";

export const BLOCK_MS = 60_000;
export const JUMP_INTERVAL_MS = 15 * 60_000;
export const MAX_OFFSET_MS = 24 * 60 * 60_000;

export interface TimedBlock {
  startMs: number;
  id: string;
  text: string;
}

export interface TranscriptLayout {
  blocks: TimedBlock[];
  untimed: { id: string; text: string } | null;
  jumps: TimedBlock[];
}

export function transcriptAnchorPrefix(recordingNumber: number): string {
  return `transcript-${recordingNumber}`;
}

export function blockId(prefix: string, startMs: number): string {
  return `${prefix}-${Math.floor(startMs / 1000)}`;
}

function isUsableOffset(ms: number): boolean {
  return Number.isFinite(ms) && ms >= 0 && ms <= MAX_OFFSET_MS;
}

function joinText(parts: string[]): string {
  return parts.join(" ").replace(/\s+/g, " ");
}

export function layoutTranscript(
  segments: readonly TranscriptSegment[],
  prefix: string,
): TranscriptLayout {
  const spoken = segments
    .map((segment) => ({
      startMs: segment.startMs,
      text: typeof segment.text === "string" ? segment.text.trim() : "",
    }))
    .filter((segment) => segment.text !== "");

  const timed = spoken
    .filter((segment) => isUsableOffset(segment.startMs))
    .sort((a, b) => a.startMs - b.startMs);
  const untimedText = spoken
    .filter((segment) => !isUsableOffset(segment.startMs))
    .map((segment) => segment.text);

  const groups: { startMs: number; parts: string[] }[] = [];
  for (const segment of timed) {
    const current = groups.at(-1);
    if (
      current === undefined ||
      segment.startMs >= current.startMs + BLOCK_MS
    ) {
      groups.push({ startMs: segment.startMs, parts: [segment.text] });
    } else {
      current.parts.push(segment.text);
    }
  }

  const blocks = groups.map((group) => ({
    startMs: group.startMs,
    id: blockId(prefix, group.startMs),
    text: joinText(group.parts),
  }));

  const jumps: TimedBlock[] = [];
  for (const block of blocks) {
    const previous = jumps.at(-1);
    if (
      previous === undefined ||
      Math.floor(block.startMs / JUMP_INTERVAL_MS) >
        Math.floor(previous.startMs / JUMP_INTERVAL_MS)
    ) {
      jumps.push(block);
    }
  }

  return {
    blocks,
    untimed:
      untimedText.length === 0
        ? null
        : { id: `${prefix}-untimed`, text: joinText(untimedText) },
    jumps: jumps.length < 2 ? [] : jumps,
  };
}
