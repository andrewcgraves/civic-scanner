import type { MeetingDetail } from "@/lib/api/client";

export async function TranscriptSection({
  meeting,
}: {
  meeting: MeetingDetail;
}) {
  await Promise.resolve(meeting);
  return null;
}
