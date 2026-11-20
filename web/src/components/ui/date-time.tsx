import {
  formatMeetingDate,
  formatMeetingDateTime,
  formatMeetingTime,
  formatOffset,
  formatShortDate,
  formatShortDateTime,
  offsetDuration,
} from "@/lib/datetime";

const FORMATS = {
  datetime: formatMeetingDateTime,
  date: formatMeetingDate,
  time: formatMeetingTime,
  "short-date": formatShortDate,
  "short-datetime": formatShortDateTime,
} as const;

export type MeetingTimeFormat = keyof typeof FORMATS;

export function MeetingTime({
  value,
  format = "datetime",
}: {
  value: string;
  format?: MeetingTimeFormat;
}) {
  return (
    <time dateTime={value} className="tabular-nums">
      {FORMATS[format](value)}
    </time>
  );
}

export function Offset({ ms }: { ms: number }) {
  return (
    <time dateTime={offsetDuration(ms)} className="tabular-nums">
      {formatOffset(ms)}
    </time>
  );
}
