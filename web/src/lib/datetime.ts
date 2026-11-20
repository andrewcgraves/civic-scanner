export const MEETING_TIME_ZONE = "America/Los_Angeles";

const LOCALE = "en-US";

type Parts = Record<Intl.DateTimeFormatPartTypes, string>;

const longFormat = new Intl.DateTimeFormat(LOCALE, {
  timeZone: MEETING_TIME_ZONE,
  weekday: "long",
  month: "long",
  day: "numeric",
  year: "numeric",
  hour: "numeric",
  minute: "2-digit",
  hour12: true,
  timeZoneName: "short",
});

const shortFormat = new Intl.DateTimeFormat(LOCALE, {
  timeZone: MEETING_TIME_ZONE,
  weekday: "short",
  month: "short",
  day: "numeric",
  year: "numeric",
});

export const UNKNOWN_DATE = "Date unknown";

function isValidDate(iso: string): boolean {
  return !Number.isNaN(Date.parse(iso));
}

function partsOf(format: Intl.DateTimeFormat, iso: string): Parts {
  const parts = {} as Parts;
  for (const part of format.formatToParts(new Date(iso))) {
    parts[part.type] = part.value;
  }
  return parts;
}

export function formatMeetingDate(iso: string): string {
  if (!isValidDate(iso)) {
    return UNKNOWN_DATE;
  }
  const p = partsOf(longFormat, iso);
  return `${p.weekday}, ${p.month} ${p.day}, ${p.year}`;
}

export function formatMeetingTime(iso: string): string {
  if (!isValidDate(iso)) {
    return UNKNOWN_DATE;
  }
  const p = partsOf(longFormat, iso);
  return `${p.hour}:${p.minute} ${p.dayPeriod.toUpperCase()} ${p.timeZoneName}`;
}

export function formatMeetingDateTime(iso: string): string {
  if (!isValidDate(iso)) {
    return UNKNOWN_DATE;
  }
  return `${formatMeetingDate(iso)} at ${formatMeetingTime(iso)}`;
}

export function formatShortDate(iso: string): string {
  if (!isValidDate(iso)) {
    return UNKNOWN_DATE;
  }
  const p = partsOf(shortFormat, iso);
  return `${p.weekday}, ${p.month} ${p.day}, ${p.year}`;
}

export function formatShortDateTime(iso: string): string {
  if (!isValidDate(iso)) {
    return UNKNOWN_DATE;
  }
  return `${formatShortDate(iso)}, ${formatMeetingTime(iso)}`;
}

export function isUpcoming(iso: string, now: Date): boolean {
  return Date.parse(iso) >= now.getTime();
}

function wholeSeconds(ms: number): number {
  return Math.max(0, Math.floor(ms / 1000));
}

export function formatOffset(ms: number): string {
  const total = wholeSeconds(ms);
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const seconds = String(total % 60).padStart(2, "0");
  if (hours === 0) {
    return `${minutes}:${seconds}`;
  }
  return `${hours}:${String(minutes).padStart(2, "0")}:${seconds}`;
}

export function offsetDuration(ms: number): string {
  const total = wholeSeconds(ms);
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const seconds = total % 60;
  return `PT${hours}H${minutes}M${seconds}S`;
}
