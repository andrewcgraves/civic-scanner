package agendadiff_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/agendadiff"
)

const itemsWithBodies = `REGULAR BOARD MEETING
Monday, January 12, 2027 – 9:30 a.m.
Consent Calendar
1. Approval of Minutes
2. Bus Maintenance Agreement with Transdev for $1,250,000
3. Fare Study Update
Staff Reports
1. Approval of Minutes
Recommended action: approve
2. Bus Maintenance Agreement with Transdev for $1,250,000
Recommended action: approve
3. Fare Study Update
Recommended action: approve
Adjournment`

const paginatedItems = `REGULAR BOARD MEETING
Monday, January 12, 2027 – 9:30 a.m.
1. Approval of Minutes
Approve the minutes of the December 8, 2026 meeting.
2. Bus Maintenance Agreement
Recommendation: Receive and file.
Page 1 of 3
Board Agenda – January 12, 2027
3. Fare Study Update
Staff will present the fare study.
Recommendation: Receive and file.
4. Legislative Report
Recommendation: Receive and file.
Page 2 of 3
Board Agenda – January 12, 2027
5. Streetcar Update
Staff will present an update.
Adjournment
Page 3 of 3`

const withLinks = `# REGULAR BOARD MEETING

Join by [Zoom](https://zoom.us/j/81234567890) or in person.

1\. Bus contract, see the [staff report](https://octa.net/reports/item-1-v1.pdf).`

type pair struct {
	name     string
	old, new string
}

func reviewPairs(t *testing.T) []pair {
	t.Helper()
	return []pair{
		{
			"an item added whose title is in the list and as a heading", itemsWithBodies,
			replaceOnce(t, replaceOnce(t, itemsWithBodies, "3. Fare Study Update\nStaff Reports", "3. Fare Study Update\n4. Streetcar Update\nStaff Reports"),
				"Recommended action: approve\nAdjournment", "Recommended action: approve\n4. Streetcar Update\nAdjournment"),
		},
		{"an item removed whose title is in the list and as a heading", replaceOnce(t, replaceOnce(t, itemsWithBodies, "3. Fare Study Update\nStaff Reports", "3. Fare Study Update\n4. Streetcar Update\nStaff Reports"),
			"Recommended action: approve\nAdjournment", "Recommended action: approve\n4. Streetcar Update\nAdjournment"), itemsWithBodies},
		{"an amount changed on the list line and the body line", itemsWithBodies, strings.ReplaceAll(itemsWithBodies, "$1,250,000", "$1,500,000")},
		{"a running-header date changed on every page", paginatedItems, strings.ReplaceAll(paginatedItems, "Board Agenda – January 12, 2027", "Board Agenda – January 19, 2027")},
		{"a recommended action changed on every copy", itemsWithBodies, strings.ReplaceAll(itemsWithBodies, "Recommended action: approve", "Recommended action: deny")},
		{"a line that appeared twice now appears once", itemsWithBodies, replaceOnce(t, itemsWithBodies, "3. Fare Study Update\nRecommended action: approve\n", "3. Fare Study Update\n")},
		{"a removed page whose lines repeat elsewhere", paginatedItems, replaceOnce(t, paginatedItems,
			"3. Fare Study Update\nStaff will present the fare study.\nRecommendation: Receive and file.\n4. Legislative Report\nRecommendation: Receive and file.\nPage 2 of 3\nBoard Agenda – January 12, 2027\n", "")},
		{"a cancelled item whose title appeared twice", itemsWithBodies, strings.ReplaceAll(itemsWithBodies, "3. Fare Study Update", "3. Fare Study Update – CANCELLED")},
		{"a negative amount became positive", "Budget adjustment: -$5,000 for fuel", "Budget adjustment: +$5,000 for fuel"},
		{"an accounting negative became positive", "Fund balance change (5,000)", "Fund balance change 5,000"},
		{"a comparison flipped", "Routes with ridership ≤ 50 per day", "Routes with ridership ≥ 50 per day"},
		{"a percentage became a number", "Fare increase of 5% effective March 1", "Fare increase of 5 effective March 1"},
		{"a misplaced comma moved a digit", "Contract amount 1,250", "Contract amount 12,50"},
		{"a list of items became one number", "Approve Items 3,4 on consent", "Approve Items 34 on consent"},
		{"struck-through text", "Staff recommends the Board do not approve the contract", "Staff recommends the Board do ~~not~~ approve the contract"},
		{"a Zoom link target changed", withLinks, strings.ReplaceAll(withLinks, "81234567890", "89999999999")},
		{"a staff-report link target changed", withLinks, strings.ReplaceAll(withLinks, "item-1-v1.pdf", "item-1-v2.pdf")},
		{"a page reference inside a sentence", "1. Bus contract\nSee staff report on page 12\n2. Adjournment", "1. Bus contract\nSee staff report on page 40\n2. Adjournment"},
		{"a capitalised page reference at the end of a sentence", "1. Bus contract\nSee the staff report on Page 12\n2. Adjournment", "1. Bus contract\nSee the staff report on Page 40\n2. Adjournment"},
		{"a year alone on a line", "Budget for fiscal year\n2026\nAdopt the budget", "Budget for fiscal year\n2027\nAdopt the budget"},
		{"a number alone on a line", "Number of new bus shelters\n500\nApprove the program", "Number of new bus shelters\n900\nApprove the program"},
	}
}

func TestEveryReviewedChangeIsReported(t *testing.T) {
	for _, tc := range reviewPairs(t) {
		t.Run(tc.name, func(t *testing.T) {
			old, updated := normalized(tc.old), normalized(tc.new)
			if old == updated {
				t.Fatal("fixture texts are identical; the case tests nothing")
			}
			if agendadiff.Equivalent(t.Context(), old, updated) {
				t.Error("Equivalent = true, want a reported change")
			}
			if d := compare(t, old, updated); d.Equivalent() {
				t.Error("Compare found no change")
			}
		})
	}
}

func TestFormattingAroundTheNewRulesIsStillNoise(t *testing.T) {
	cases := []pair{
		{"a link in markdown and plain text in the PDF", "REGULAR BOARD MEETING\nJoin by Zoom or in person.\n1. Bus contract, see the staff report.", withLinks},
		{"the same link targets in two markdown renderings", withLinks, strings.ReplaceAll(withLinks, "\n\n", "\n")},
		{"page numbers in every style", paginatedItems, strings.NewReplacer("Page 1 of 3", "- 1 -", "Page 2 of 3", "2", "Page 3 of 3", "3").Replace(paginatedItems)},
		{"a header page number stripped at the end of the line", "Board Agenda – Page 2\n1. Item", "Board Agenda\n1. Item"},
		{"a well-formed thousands separator", "Award $1,250,000.00", "Award $1250000.00"},
		{"a blockquote marker", "> Staff recommends approval", "Staff recommends approval"},
		{"a bullet dash and a sign-less number", "- 1. Approve the minutes", "• 1. Approve the minutes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old, updated := normalized(tc.old), normalized(tc.new)
			if !agendadiff.Equivalent(t.Context(), old, updated) {
				t.Errorf("Equivalent = false; changes: %+v", compare(t, old, updated).Changes)
			}
		})
	}
}

func hyphenEnded(bytes int) string {
	var b strings.Builder
	for b.Len() < bytes {
		b.WriteString("approval of the transpor-\n")
	}
	return b.String()
}

func TestCanonicalisationIsLinear(t *testing.T) {
	for _, size := range []int{1_500_000 / timingScale, 3_000_000 / timingScale} {
		old := hyphenEnded(size)
		updated := old + "end of agenda\n"
		start := time.Now()
		if agendadiff.Equivalent(t.Context(), old, updated) {
			t.Errorf("%d bytes: Equivalent = true", size)
		}
		if _, err := agendadiff.Compare(t.Context(), old, updated); err != nil {
			t.Fatal(err)
		}
		elapsed := time.Since(start)
		t.Logf("%d bytes of hyphen-ended lines: Equivalent and Compare in %v", size, elapsed)
		if elapsed > 10*time.Second {
			t.Errorf("%d bytes of hyphen-ended lines took %v, want well under 10s", size, elapsed)
		}
	}
}

func TestALargeAgendaJustUnderTheCapIsComparedQuickly(t *testing.T) {
	old := largeAgenda(15_000 / timingScale)
	if len(old) > agendadiff.MaxCompareBytes {
		t.Fatalf("fixture is %d bytes, over the cap", len(old))
	}
	updated := replaceOnce(t, old, "increasing the contract value by $627,500", "increasing the contract value by $672,500")
	start := time.Now()
	equivalent := agendadiff.Equivalent(t.Context(), old, updated)
	equivalence := time.Since(start)
	d := compare(t, old, updated)
	elapsed := time.Since(start)
	t.Logf("%d bytes: Equivalent in %v, Equivalent and Compare in %v", len(old), equivalence, elapsed)
	if equivalent || len(d.Changes) != 1 {
		t.Errorf("equivalent = %v, changes = %d", equivalent, len(d.Changes))
	}
	if elapsed > 10*time.Second {
		t.Errorf("took %v", elapsed)
	}
}

func TestTextsOverTheCapAreDifferent(t *testing.T) {
	big := strings.Repeat("a", agendadiff.MaxCompareBytes+1)
	start := time.Now()
	if agendadiff.Equivalent(t.Context(), big, big+"b") {
		t.Error("Equivalent = true over the cap")
	}
	d := compare(t, big, big+"b")
	if !d.TooLarge || d.Equivalent() {
		t.Errorf("diff = %+v, want too large and not equivalent", d)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("over the cap took %v", elapsed)
	}
}

func TestACancelledContextStopsTheComparison(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	text := hyphenEnded(100_000)
	if agendadiff.Equivalent(ctx, text, text+"x") {
		t.Error("Equivalent = true after cancel")
	}
	if _, err := agendadiff.Compare(ctx, text, text+"x"); err == nil {
		t.Error("Compare err = nil after cancel")
	}
	long := strings.Repeat("word\n", 20)
	if _, err := agendadiff.Compare(ctx, long, long+"x"); err == nil {
		t.Error("Compare of a short text err = nil after cancel")
	}
}
