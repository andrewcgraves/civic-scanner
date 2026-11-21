import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { Delivery, DeliveryAttempt } from "../deliveries";

import { DeliveryLog } from "./delivery-log";

function attempt(
  number: number,
  overrides: Partial<DeliveryAttempt> = {},
): DeliveryAttempt {
  return {
    attempt: number,
    startedAt: "2026-11-17T17:38:00Z",
    durationMs: 412,
    statusCode: 503,
    error: null,
    succeeded: false,
    ...overrides,
  };
}

function delivery(overrides: Partial<Delivery> = {}): Delivery {
  return {
    eventId: "11111111-0000-4000-8000-000000000001",
    type: "agenda_changed",
    meeting: {
      id: "22222222-0000-4000-8000-000000000001",
      title: "Board of Directors",
      startsAt: "2026-12-03T18:00:00Z",
    },
    succeeded: false,
    firstAttemptAt: "2026-11-17T17:38:00Z",
    lastAttemptAt: "2026-11-18T17:40:00Z",
    attempts: [
      attempt(1),
      attempt(2, {
        startedAt: "2026-11-18T17:40:00Z",
        statusCode: null,
        error: "the endpoint did not answer in time",
        durationMs: 10_000,
      }),
    ],
    ...overrides,
  };
}

describe("DeliveryLog", () => {
  it("lists a failed delivery with every attempt", () => {
    render(
      <DeliveryLog
        deliveries={[delivery()]}
        agencyName="LA Metro"
        olderHref={null}
      />,
    );

    const item = screen.getByRole("listitem");
    expect(item).toHaveTextContent("Failed");
    expect(item).toHaveTextContent(
      /Agenda changed · Board of Directors, Thu, Dec 3, 2026/,
    );
    expect(item).toHaveTextContent(
      /2 attempts, Tue, Nov 17, 2026, 9:38.AM PST to Wed, Nov 18, 2026, 9:40.AM PST · last: No answer: the endpoint did not answer in time/,
    );
    expect(item).toHaveTextContent("Attempts (2)");
    const rows = within(item).getAllByRole("row");
    expect(rows.map((row) => row.textContent)).toEqual([
      "#SentResultTook",
      expect.stringMatching(
        /^1Tue, Nov 17, 2026, 9:38.AM PSTAnswered 503412 ms$/,
      ),
      expect.stringMatching(
        /^2Wed, Nov 18, 2026, 9:40.AM PSTNo answer: the endpoint did not answer in time10\.0 s$/,
      ),
    ]);
    expect(screen.queryByRole("link")).toBeNull();
  });

  it("names the attempt that delivered", () => {
    render(
      <DeliveryLog
        deliveries={[
          delivery({
            type: "transcript_ready",
            succeeded: true,
            attempts: [
              attempt(1),
              attempt(2, {
                startedAt: "2026-11-13T14:21:00Z",
                statusCode: 204,
                durationMs: 240,
                succeeded: true,
              }),
            ],
          }),
        ]}
        agencyName="LA Metro"
        olderHref={null}
      />,
    );

    const item = screen.getByRole("listitem");
    expect(item).toHaveTextContent("Delivered");
    expect(item).toHaveTextContent("Transcript ready");
    expect(item).toHaveTextContent(
      /Fri, Nov 13, 2026, 6:21.AM PST · 204 in 240 ms on attempt 2/,
    );
  });

  it("gives one time for a single attempt and copes with none", () => {
    render(
      <DeliveryLog
        deliveries={[
          delivery({ attempts: [attempt(1)] }),
          delivery({
            eventId: "11111111-0000-4000-8000-000000000002",
            type: "meeting_moved" as Delivery["type"],
            attempts: [],
          }),
        ]}
        agencyName="LA Metro"
        olderHref={null}
      />,
    );

    const [single, none] = screen.getAllByRole("listitem") as [
      HTMLElement,
      HTMLElement,
    ];
    expect(single).toHaveTextContent(
      /1 attempt, Wed, Nov 18, 2026, 9:40.AM PST · last: Answered 503/,
    );
    expect(single).not.toHaveTextContent(" to ");
    expect(none).toHaveTextContent("meeting_moved");
    expect(none).toHaveTextContent(
      /0 attempts, Wed, Nov 18, 2026, 9:40.AM PST$/,
    );
    expect(within(none).queryByRole("table")).toBeNull();
  });

  it("links to older deliveries when there are more", () => {
    render(
      <DeliveryLog
        deliveries={[delivery()]}
        agencyName="LA Metro"
        olderHref="/dashboard/subscriptions/x?cursor=abc"
      />,
    );

    expect(
      screen.getByRole("link", { name: "Older deliveries" }),
    ).toHaveAttribute("href", "/dashboard/subscriptions/x?cursor=abc");
  });

  it("says when nothing has been delivered yet", () => {
    render(
      <DeliveryLog deliveries={[]} agencyName="LA Metro" olderHref={null} />,
    );

    expect(screen.getByText("No deliveries yet")).toBeVisible();
    expect(
      screen.getByText(/once LA Metro posts a meeting or an agenda/),
    ).toBeVisible();
    expect(screen.queryByRole("list")).toBeNull();
  });
});
