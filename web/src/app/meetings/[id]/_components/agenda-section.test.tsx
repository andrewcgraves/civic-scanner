import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { Agenda, MeetingStatus } from "@/lib/api/client";

import { AGENDA_SECTION_ID, AgendaSection } from "./agenda-section";

const AGENDA: Agenda = {
  version: 1,
  documentUrl: "https://metro.legistar.com/agenda.pdf",
  summary:
    "The board will vote on the budget.\n\nIt will also hear public comment.",
  changeNote: null,
  foundAt: "2027-01-13T18:30:00Z",
};

function renderAgenda(
  agenda: Agenda | null,
  status: MeetingStatus = "scheduled",
) {
  render(
    <AgendaSection agenda={agenda} agencyName="LA Metro" status={status} />,
  );
  return screen.getByRole("region", { name: "Agenda" });
}

function originalLink() {
  return screen.getByRole("link", {
    name: /Read the original agenda from LA Metro/,
  });
}

describe("AgendaSection", () => {
  it("says no agenda has been found when there is none", () => {
    const section = renderAgenda(null);

    expect(section).toHaveAttribute("id", AGENDA_SECTION_ID);
    expect(within(section).getByText("No agenda yet")).toBeInTheDocument();
    expect(
      within(section).getByText(
        /Civic Scanner has not found an agenda for this meeting\./,
      ),
    ).toBeInTheDocument();
    expect(within(section).queryByRole("link")).not.toBeInTheDocument();
  });

  it("does not promise an agenda for a cancelled meeting without one", () => {
    const section = renderAgenda(null, "cancelled");

    expect(within(section).getByText("No agenda")).toBeInTheDocument();
    expect(
      within(section).getByText(
        "Civic Scanner did not find an agenda for this meeting before it was cancelled.",
      ),
    ).toBeInTheDocument();
    expect(within(section).queryByText(/will appear here/)).toBeNull();
  });

  it("shows the AI summary with a link to the original agenda", () => {
    const section = renderAgenda(AGENDA);

    expect(originalLink()).toHaveAttribute("href", AGENDA.documentUrl);
    expect(originalLink()).toHaveAttribute(
      "rel",
      "external noopener noreferrer",
    );
    expect(
      within(section).getByText(/This version was found on/),
    ).toHaveTextContent(
      "This version was found on Wednesday, January 13, 2027 at 10:30 AM PST.",
    );
    expect(within(section).queryByText(/Revised agenda/)).toBeNull();
    const summary = screen.getByRole("region", { name: "Summary" });
    expect(within(summary).getByText("AI-generated")).toBeInTheDocument();
    expect(
      within(summary).getByText("The board will vote on the budget."),
    ).toBeInTheDocument();
    expect(
      within(summary).getByText("It will also hear public comment."),
    ).toBeInTheDocument();
    expect(
      within(summary).getByRole("link", {
        name: /the agency's original agenda/,
      }),
    ).toHaveAttribute("href", AGENDA.documentUrl);
    expect(
      screen.queryByRole("region", { name: /What changed/ }),
    ).not.toBeInTheDocument();
  });

  it("keeps the truncation line of a summary of a long agenda", () => {
    renderAgenda({
      ...AGENDA,
      summary:
        "The board will vote on the budget.\nThis summary covers only the first part of the agenda.",
    });

    expect(
      screen.getByText(
        "This summary covers only the first part of the agenda.",
      ),
    ).toBeInTheDocument();
  });

  it("links the original agenda while the summary is not written yet", () => {
    renderAgenda({ ...AGENDA, summary: null });

    expect(originalLink()).toHaveAttribute("href", AGENDA.documentUrl);
    const summary = screen.getByRole("region", { name: "Summary" });
    expect(
      within(summary).getByText(
        /The AI summary of this agenda has not been written yet\./,
      ),
    ).toBeInTheDocument();
    expect(within(summary).queryByText("AI-generated")).toBeNull();
  });

  it("shows the change note of a revised agenda apart from the summary", () => {
    renderAgenda({
      ...AGENDA,
      version: 3,
      changeNote: "Item 14 was added.",
    });

    expect(screen.getByText(/Revised agenda, version 3\./)).toHaveTextContent(
      "Revised agenda, version 3. This version was found on Wednesday, January 13, 2027 at 10:30 AM PST.",
    );
    const changes = screen.getByRole("region", {
      name: "What changed in version 3",
    });
    expect(within(changes).getByText("AI-generated")).toBeInTheDocument();
    expect(within(changes).getByText("Item 14 was added.")).toBeInTheDocument();
    expect(
      within(changes).getByText(/Written automatically, usually by AI/),
    ).toBeInTheDocument();
    expect(
      within(changes).queryByText("The board will vote on the budget."),
    ).toBeNull();
    const summary = screen.getByRole("region", { name: "Summary" });
    expect(within(summary).queryByText("Item 14 was added.")).toBeNull();
  });

  it("says no description of the change is available without claiming nothing changed", () => {
    renderAgenda({ ...AGENDA, version: 2, changeNote: null });

    const changes = screen.getByRole("region", {
      name: "What changed in version 2",
    });
    expect(changes).toHaveTextContent(
      "The agency revised this agenda. No description of what changed from version 1 is available; the original agenda shows the current version in full.",
    );
    expect(within(changes).queryByText("AI-generated")).toBeNull();
    expect(changes).not.toHaveTextContent(/nothing changed/i);
  });

  it("does not link a document URL that is not a web address", () => {
    const section = renderAgenda({
      ...AGENDA,
      documentUrl: "javascript:alert(1)",
    });

    expect(within(section).queryByRole("link")).not.toBeInTheDocument();
    expect(
      within(section).getByText(
        "The address LA Metro gave for the original agenda is not a web link, so it cannot be opened from here.",
      ),
    ).toBeInTheDocument();
    expect(
      within(section).getByText("the agency's original agenda"),
    ).toBeInTheDocument();
  });
});
