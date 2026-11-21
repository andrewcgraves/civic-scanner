export const REVIEW_PATH = "/admin/agency-submissions";

export const REVIEW_SIGN_IN_PATH = `/auth/sign-in?returnTo=${encodeURIComponent(REVIEW_PATH)}`;

export const SLUG_MIN_LENGTH = 2;
export const SLUG_MAX_LENGTH = 60;

const SUBMITTED_PREFIX = "submitted-";
const SLUG_PATTERN = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;
const UUID_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const ECHO_LIMIT = 200;
const MESSAGE_LIMIT = 300;

export const SLUG_RULE = `Use ${String(SLUG_MIN_LENGTH)} to ${String(SLUG_MAX_LENGTH)} lower-case letters, digits and single hyphens, not starting with “${SUBMITTED_PREFIX}”.`;

export type ReviewState =
  | { kind: "idle"; slug: string }
  | { kind: "invalid-slug"; slug: string; message: string }
  | { kind: "slug-taken"; slug: string }
  | { kind: "reviewed"; slug: string }
  | { kind: "gone"; slug: string }
  | { kind: "not-allowed"; slug: string }
  | { kind: "unavailable"; slug: string };

export type ReviewOutcome =
  { kind: "approved"; slug: string } | { kind: "rejected" };

export function isLiveSlug(slug: string): boolean {
  return (
    slug.length >= SLUG_MIN_LENGTH &&
    slug.length <= SLUG_MAX_LENGTH &&
    SLUG_PATTERN.test(slug) &&
    !slug.startsWith(SUBMITTED_PREFIX)
  );
}

export function isSubmissionId(value: string): boolean {
  return UUID_PATTERN.test(value);
}

export function suggestSlug(name: string): string {
  const base = name
    .normalize("NFKD")
    .replace(/\p{M}/gu, "")
    .toLowerCase()
    .replace(/&/g, " and ")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, SLUG_MAX_LENGTH)
    .replace(/-+$/, "");
  return isLiveSlug(base) ? base : "";
}

function text(value: FormDataEntryValue | null): string {
  return typeof value === "string" ? value : "";
}

export function readReview(formData: FormData): { id: string; slug: string } {
  return {
    id: text(formData.get("id")).trim(),
    slug: text(formData.get("slug")).trim().slice(0, ECHO_LIMIT),
  };
}

export function errorCode(body: unknown): string | null {
  if (typeof body !== "object" || body === null) {
    return null;
  }
  const { code } = body as { code?: unknown };
  return typeof code === "string" ? code : null;
}

export function errorMessage(body: unknown): string | null {
  if (typeof body !== "object" || body === null) {
    return null;
  }
  const { message } = body as { message?: unknown };
  return typeof message === "string" && message !== ""
    ? message.slice(0, MESSAGE_LIMIT)
    : null;
}

export function approvedPath(slug: string): string {
  return `${REVIEW_PATH}?approved=${encodeURIComponent(slug)}`;
}

export const REJECTED_PATH = `${REVIEW_PATH}?rejected=1`;

function single(value: string | string[] | undefined): string | undefined {
  return typeof value === "string" ? value : undefined;
}

export function readOutcome(
  params: Record<string, string | string[] | undefined>,
): ReviewOutcome | null {
  const approved = single(params.approved);
  if (approved !== undefined && isLiveSlug(approved)) {
    return { kind: "approved", slug: approved };
  }
  if (single(params.rejected) === "1") {
    return { kind: "rejected" };
  }
  return null;
}
