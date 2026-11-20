export const SUBMIT_AGENCY_PATH = "/submit-agency";

export const SIGN_IN_PATH = `/auth/sign-in?returnTo=${encodeURIComponent(SUBMIT_AGENCY_PATH)}`;

export const NAME_MAX_LENGTH = 200;
export const WEBSITE_MAX_LENGTH = 2048;

const ECHO_LIMIT = 4096;
const MESSAGE_LIMIT = 300;

export type SubmissionField = "name" | "website";

export type SubmissionValues = Record<SubmissionField, string>;

export type FieldErrors = Partial<Record<SubmissionField, string>>;

export type SubmissionState =
  | { kind: "idle"; values: SubmissionValues }
  | { kind: "submitted"; name: string; website: string }
  | {
      kind: "invalid";
      values: SubmissionValues;
      errors: FieldErrors;
      message?: string;
    }
  | { kind: "on-file"; values: SubmissionValues }
  | { kind: "limit"; values: SubmissionValues }
  | { kind: "closed"; values: SubmissionValues }
  | { kind: "unavailable"; values: SubmissionValues };

export const INITIAL_STATE: SubmissionState = {
  kind: "idle",
  values: { name: "", website: "" },
};

export const FIELD_LABELS: Record<SubmissionField, string> = {
  name: "Agency name",
  website: "Website",
};

export const BODY_ERROR = "Check the name and website and try again.";

function text(value: FormDataEntryValue | null): string {
  return typeof value === "string" ? value.slice(0, ECHO_LIMIT) : "";
}

export function readValues(formData: FormData): SubmissionValues {
  return {
    name: text(formData.get("name")),
    website: text(formData.get("website")),
  };
}

export function checkValues(values: SubmissionValues): FieldErrors {
  const errors: FieldErrors = {};
  const name = values.name.trim().replace(/\s+/gu, " ");
  const website = values.website.trim();
  if (name === "") {
    errors.name = "Enter the agency's name.";
  } else if ([...name].length > NAME_MAX_LENGTH) {
    errors.name = `Name must be at most ${String(NAME_MAX_LENGTH)} characters.`;
  }
  if (website === "") {
    errors.website = "Enter the agency's website.";
  } else if (new TextEncoder().encode(website).length > WEBSITE_MAX_LENGTH) {
    errors.website = "Website must be at most 2,048 characters.";
  }
  return errors;
}

function isField(value: unknown): value is SubmissionField {
  return value === "name" || value === "website";
}

export function fieldErrorsFrom(body: unknown): FieldErrors {
  const errors: FieldErrors = {};
  if (typeof body !== "object" || body === null) {
    return errors;
  }
  const { fields } = body as { fields?: unknown };
  if (!Array.isArray(fields)) {
    return errors;
  }
  for (const entry of fields as unknown[]) {
    if (typeof entry !== "object" || entry === null) {
      continue;
    }
    const { field, message } = entry as { field?: unknown; message?: unknown };
    if (isField(field) && typeof message === "string" && message !== "") {
      errors[field] ??= message.slice(0, MESSAGE_LIMIT);
    }
  }
  return errors;
}
