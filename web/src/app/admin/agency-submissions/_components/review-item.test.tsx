import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { ReviewState } from "../state";

const mocks = vi.hoisted(() => ({
  approval: undefined as ReviewState | undefined,
  rejection: undefined as ReviewState | undefined,
  pending: undefined as Promise<void> | undefined,
  approved: [] as FormData[],
  rejected: [] as FormData[],
}));

vi.mock("../actions", () => ({
  approveSubmission: async (_previous: ReviewState, formData: FormData) => {
    mocks.approved.push(formData);
    if (mocks.pending !== undefined) {
      await mocks.pending;
    }
    return mocks.approval;
  },
  rejectSubmission: async (_previous: ReviewState, formData: FormData) => {
    mocks.rejected.push(formData);
    if (mocks.pending !== undefined) {
      await mocks.pending;
    }
    return mocks.rejection;
  },
}));

const { ReviewItem } = await import("./review-item");

const SUBMISSION = {
  id: "8b0e6c43-3f3b-4f39-9a31-2a1f7c1a9e10",
  name: "Big Bear Transit",
  website: "https://www.bigbear.example.org/board",
  submittedAt: "2026-11-19T18:30:00Z",
  submittedBy: {
    id: "1d1f4c1e-5f0b-4b9b-8f5e-0c3c2a6f9a11",
    email: "ada@example.com",
  },
};

async function click(name: string) {
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name }));
    await Promise.resolve();
  });
}

describe("ReviewItem", () => {
  beforeEach(() => {
    mocks.approval = undefined;
    mocks.rejection = undefined;
    mocks.pending = undefined;
    mocks.approved = [];
    mocks.rejected = [];
  });

  it("shows the submission with the website as text", () => {
    render(<ReviewItem submission={SUBMISSION} />);

    expect(
      screen.getByRole("article", { name: "Big Bear Transit" }),
    ).toBeVisible();
    expect(
      screen.getByText("https://www.bigbear.example.org/board"),
    ).toBeVisible();
    expect(screen.queryByRole("link")).toBeNull();
    expect(screen.getByText("ada@example.com")).toBeVisible();
    expect(screen.getByText(/Nov 19, 2026, 10:30 AM PST/)).toHaveAttribute(
      "datetime",
      SUBMISSION.submittedAt,
    );
  });

  it("says when the submitter's email is unknown", () => {
    render(
      <ReviewItem
        submission={{
          ...SUBMISSION,
          submittedBy: { ...SUBMISSION.submittedBy, email: null },
        }}
      />,
    );
    expect(screen.getByText("Unknown email")).toBeVisible();
  });

  it("suggests a slug from the name and sends what the admin typed", async () => {
    render(<ReviewItem submission={SUBMISSION} />);
    const slug = screen.getByLabelText("Slug");
    expect(slug).toHaveValue("big-bear-transit");
    expect(slug).toBeRequired();
    expect(slug).toHaveAttribute("maxLength", "60");

    fireEvent.change(slug, { target: { value: "bbt" } });
    mocks.approval = { kind: "slug-taken", slug: "bbt" };
    await click("Approve and make live");

    expect(mocks.approved).toHaveLength(1);
    expect(mocks.approved[0]?.get("id")).toBe(SUBMISSION.id);
    expect(mocks.approved[0]?.get("slug")).toBe("bbt");
    const field = screen.getByLabelText("Slug");
    expect(field).toHaveValue("bbt");
    expect(field).toHaveAttribute("aria-invalid", "true");
    expect(field).toHaveAccessibleDescription(
      expect.stringContaining("Another agency already has this slug") as string,
    );
  });

  it("shows the API's message for an invalid slug", async () => {
    render(<ReviewItem submission={SUBMISSION} />);
    mocks.approval = {
      kind: "invalid-slug",
      slug: "x",
      message: "the slug is bad",
    };
    await click("Approve and make live");

    expect(screen.getByText("the slug is bad")).toBeVisible();
  });

  it("rejects with the submission's id", async () => {
    render(<ReviewItem submission={SUBMISSION} />);
    mocks.rejection = { kind: "reviewed", slug: "" };
    await click("Reject");

    expect(mocks.rejected[0]?.get("id")).toBe(SUBMISSION.id);
    expect(screen.getByRole("alert")).toHaveTextContent(
      "already been reviewed",
    );
    expect(screen.getByLabelText("Slug")).not.toHaveAttribute("aria-invalid");
  });

  it.each<[ReviewState["kind"], string]>([
    ["gone", "could not be found"],
    ["not-allowed", "can no longer review"],
    ["unavailable", "could not be saved"],
  ])("explains %s", async (kind, text) => {
    render(<ReviewItem submission={SUBMISSION} />);
    mocks.approval = { kind, slug: "big-bear" } as ReviewState;
    await click("Approve and make live");

    expect(screen.getByRole("alert")).toHaveTextContent(text);
  });

  it("disables both buttons while a review is being saved", async () => {
    let finish = () => undefined as void;
    mocks.pending = new Promise<void>((resolve) => {
      finish = resolve;
    });
    mocks.approval = { kind: "unavailable", slug: "big-bear-transit" };
    render(<ReviewItem submission={SUBMISSION} />);
    await click("Approve and make live");

    expect(screen.getByRole("button", { name: "Approving…" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Reject" })).toBeDisabled();

    await act(async () => {
      finish();
      await mocks.pending;
    });
    expect(
      screen.getByRole("button", { name: "Approve and make live" }),
    ).toBeEnabled();
  });
});
