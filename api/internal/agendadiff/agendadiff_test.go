package agendadiff_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/agendadiff"
	"github.com/andrewcgraves/civic-scanner/api/internal/extract"
)

const directPDF = `ORANGE COUNTY TRANSPORTATION AUTHORITY
BOARD OF DIRECTORS REGULAR MEETING AGENDA
Monday, January 12, 2027 – 9:30 a.m.
Board Room, 550 South Main Street, Orange, California
Call to Order
Roll Call
Public Comments
At this time, members of the public may address the Board of Directors regarding any item
within the subject matter jurisdiction of the Board. Comments are limited to three min-
utes per speaker.
Consent Calendar (Items 1 through 4)
• 1. Approval of Minutes
Approve the minutes of the December 8, 2026 Board of Directors meeting.
• 2. Agreement for Bus Maintenance Services
Recommendation: Authorize the Chief Executive Officer to execute Agreement No. C-6-2214
with Transdev Services, Inc., in the amount of $1,250,000, for bus maintenance services.
Page 1 of 3
OCTA Board of Directors Agenda – January 12, 2027
• 3. Fiscal Year 2026-27 First Quarter Budget Status Report
Recommendation: Receive and file as an information item.
• 4. State Legislative Status Report
Recommendation: Adopt a support position on SB 71 (Wiener), regarding transit-
oriented development near high-capacity rail stations.
Regular Calendar
5. OC Streetcar Project Update
Staff will present an update on testing and the anticipated start of revenue service in
spring 2027. See the staff report for details.
Page 2 of 3
OCTA Board of Directors Agenda – January 12, 2027
6. Public Hearing on Proposed Fare Changes
Recommendation: Hold a public hearing on the proposed increase of the base fare from
$2.00 to $2.25, effective March 1, 2027.
Chief Executive Officer's Report
Directors' Reports
Adjournment
The next regular meeting will be held at 9:30 a.m. on Monday, January 26, 2027.
Page 3 of 3`

const firecrawlMarkdown = `# ORANGE COUNTY TRANSPORTATION AUTHORITY

## BOARD OF DIRECTORS REGULAR MEETING AGENDA

**Monday, January 12, 2027 – 9:30 a.m.**

Board Room, 550 South Main Street, Orange, California

## Call to Order

## Roll Call

## Public Comments

At this time, members of the public may address the Board of Directors regarding any item within the subject matter jurisdiction of the Board. Comments are limited to three minutes per speaker.

### Consent Calendar (Items 1 through 4)

- **1\. Approval of Minutes**

  Approve the minutes of the December 8, 2026 Board of Directors meeting.

- **2\. Agreement for Bus Maintenance Services**

  _Recommendation:_ Authorize the Chief Executive Officer to execute Agreement No. C-6-2214 with Transdev Services, Inc., in the amount of $1,250,000, for bus maintenance services.

- **3\. Fiscal Year 2026-27 First Quarter Budget Status Report**

  _Recommendation:_ Receive and file as an information item.

- **4\. State Legislative Status Report**

  _Recommendation:_ Adopt a support position on SB 71 (Wiener), regarding transit-oriented development near high-capacity rail stations.

### Regular Calendar

| 5. | OC Streetcar Project Update |
| --- | --- |
| | Staff will present an update on testing and the anticipated start of revenue service in spring 2027. See the [staff report](https://octa.net/agendas/2027-01-12/item-5.pdf) for details. |

| 6. | Public Hearing on Proposed Fare Changes |
| --- | --- |
| | _Recommendation:_ Hold a public hearing on the proposed increase of the base fare from $2.00 to $2.25, effective March 1, 2027. |

## Chief Executive Officer’s Report

## Directors’ Reports

## Adjournment

The next regular meeting will be held at 9:30 a.m. on Monday, January 26, 2027.

![OCTA logo](https://octa.net/logo.png)`

const rewrapped = `ORANGE COUNTY TRANSPORTATION AUTHORITY
BOARD OF DIRECTORS REGULAR MEETING AGENDA
Monday, January 12, 2027 – 9:30 a.m.
Board Room, 550 South Main Street, Orange, California
Call to Order
Roll Call
Public Comments
At this time, members of the public may address the Board of
Directors regarding any item within the subject matter jurisdic-
tion of the Board. Comments are limited to three minutes per speaker.
Consent Calendar (Items 1 through 4)
- 1. Approval of Minutes
Approve the minutes of the December 8, 2026 Board of Directors
meeting.
- 2. Agreement for Bus Maintenance Services
Recommendation: Authorize the Chief Executive Officer to exe-
cute Agreement No. C-6-2214 with Transdev Services, Inc., in the amount of
$1,250,000, for bus maintenance services.
- 3. Fiscal Year 2026-27 First Quarter Budget Status Report
Recommendation: Receive and file as an information item.
- 4. State Legislative Status Report
Recommendation: Adopt a support position on SB 71 (Wiener), regarding transit-oriented
development near high-capacity rail stations.
Regular Calendar
5. OC Streetcar Project Update
Staff will present an update on testing and the anticipated start of revenue
service in spring 2027. See the staff report for details.
6. Public Hearing on Proposed Fare Changes
Recommendation: Hold a public hearing on the proposed increase of the base
fare from $2.00 to $2.25, effective March 1, 2027.
Chief Executive Officer's Report
Directors' Reports
Adjournment
The next regular meeting will be held at 9:30 a.m. on Monday, January 26, 2027.`

const repaginated = `ORANGE COUNTY TRANSPORTATION AUTHORITY
BOARD OF DIRECTORS REGULAR MEETING AGENDA
Monday, January 12, 2027 – 9:30 a.m.
Board Room, 550 South Main Street, Orange, California
Call to Order
Roll Call
Public Comments
At this time, members of the public may address the Board of Directors regarding any item
within the subject matter jurisdiction of the Board. Comments are limited to three minutes per speaker.
Consent Calendar (Items 1 through 4)
o 1. Approval of Minutes
Approve the minutes of the December 8, 2026 Board of Directors meeting.
- 1 -
OCTA Board of Directors Agenda – January 12, 2027
o 2. Agreement for Bus Maintenance Services
Recommendation: Authorize the Chief Executive Officer to execute Agreement No. C-6-2214
with Transdev Services, Inc., in the amount of $1,250,000, for bus maintenance services.
o 3. Fiscal Year 2026-27 First Quarter Budget Status Report
Recommendation: Receive and file as an information item.
- 2 -
OCTA Board of Directors Agenda – January 12, 2027
o 4. State Legislative Status Report
Recommendation: Adopt a support position on SB 71 (Wiener), regarding transit-oriented development near high-capacity rail stations.
Regular Calendar
5. OC Streetcar Project Update
Staff will present an update on testing and the anticipated start of revenue service in
spring 2027. See the staff report for details.
3
OCTA Board of Directors Agenda – January 12, 2027
6. Public Hearing on Proposed Fare Changes
Recommendation: Hold a public hearing on the proposed increase of the base fare from
$2.00 to $2.25, effective March 1, 2027.
Chief Executive Officer's Report
Directors' Reports
Adjournment
The next regular meeting will be held at 9:30 a.m. on Monday, January 26, 2027.
4 of 4`

func normalized(s string) string {
	return extract.Normalize(s)
}

func compare(t *testing.T, old, updated string) agendadiff.Diff {
	t.Helper()
	d, err := agendadiff.Compare(t.Context(), old, updated)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	return d
}

func replaceOnce(t *testing.T, s, old, updated string) string {
	t.Helper()
	if !strings.Contains(s, old) {
		t.Fatalf("fixture does not contain %q", old)
	}
	return strings.Replace(s, old, updated, 1)
}

func largeAgenda(items int) string {
	var b strings.Builder
	b.WriteString("LOS ANGELES COUNTY METROPOLITAN TRANSPORTATION AUTHORITY\nREGULAR BOARD MEETING\nThursday, February 25, 2027, 10:00 a.m.\n")
	for i := 1; i <= items; i++ {
		fmt.Fprintf(&b, "%d. Contract modification No. %d for the Regional Connector project\n", i, 1000+i)
		fmt.Fprintf(&b, "Recommendation: Authorize the Chief Executive Officer to execute modification No. %d to Contract C%04d, increasing the contract value by $%d,500 and extending the term to June %d, 2028.\n", 1000+i, i, i*3, 1+i%28)
	}
	b.WriteString("Adjournment\n")
	return b.String()
}

func TestNoiseIsEquivalent(t *testing.T) {
	cases := []struct {
		name     string
		old, new string
	}{
		{"whitespace and hyphenation", directPDF, rewrapped},
		{"headers, footers and page numbers", directPDF, repaginated},
		{"direct PDF and Firecrawl markdown", directPDF, firecrawlMarkdown},
		{"Firecrawl markdown and direct PDF", firecrawlMarkdown, directPDF},
		{"markdown and a repaginated PDF", firecrawlMarkdown, repaginated},
		{"ligatures and HTML entities", "Approve the ﬁnancial report &amp; the audit\n" + directPDF, "Approve the financial report & the audit\n" + directPDF},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old, updated := normalized(tc.old), normalized(tc.new)
			if old == updated {
				t.Fatal("fixture texts are identical; the case tests nothing")
			}
			if !agendadiff.Equivalent(t.Context(), old, updated) {
				t.Errorf("Equivalent = false, want true; changes: %+v", compare(t, old, updated).Changes)
			}
			if d := compare(t, old, updated); !d.Equivalent() || d.Coarse {
				t.Errorf("Compare = %+v, want no changes", d)
			}
		})
	}
}

func TestRealChangesAreNotEquivalent(t *testing.T) {
	added := replaceOnce(t, directPDF, "Chief Executive Officer's Report",
		"7. Approve the 2027 Board Meeting Calendar\nRecommendation: Adopt the proposed calendar.\nChief Executive Officer's Report")
	removed := replaceOnce(t, directPDF, "• 4. State Legislative Status Report\nRecommendation: Adopt a support position on SB 71 (Wiener), regarding transit-\noriented development near high-capacity rail stations.\n", "")
	amount := replaceOnce(t, firecrawlMarkdown, "$1,250,000", "$1,500,000")
	date := replaceOnce(t, directPDF, "effective March 1, 2027", "effective April 1, 2027")
	reordered := replaceOnce(t, directPDF,
		"5. OC Streetcar Project Update\nStaff will present an update on testing and the anticipated start of revenue service in\nspring 2027. See the staff report for details.\nPage 2 of 3\nOCTA Board of Directors Agenda – January 12, 2027\n6. Public Hearing on Proposed Fare Changes\nRecommendation: Hold a public hearing on the proposed increase of the base fare from\n$2.00 to $2.25, effective March 1, 2027.",
		"6. Public Hearing on Proposed Fare Changes\nRecommendation: Hold a public hearing on the proposed increase of the base fare from\n$2.00 to $2.25, effective March 1, 2027.\nPage 2 of 3\nOCTA Board of Directors Agenda – January 12, 2027\n5. OC Streetcar Project Update\nStaff will present an update on testing and the anticipated start of revenue service in\nspring 2027. See the staff report for details.")
	large := largeAgenda(400)
	tiny := replaceOnce(t, large, "increasing the contract value by $627,500", "increasing the contract value by $672,500")

	cases := []struct {
		name          string
		old, new      string
		removed, adds []string
	}{
		{"an added item", directPDF, added, nil, []string{"7. Approve the 2027 Board Meeting Calendar", "Adopt the proposed calendar."}},
		{"a removed item", directPDF, removed, []string{"4. State Legislative Status Report", "SB 71"}, nil},
		{"a changed dollar amount", directPDF, amount, []string{"$1,250,000,"}, []string{"$1,500,000,"}},
		{"a changed date", directPDF, date, []string{"March"}, []string{"April"}},
		{"a reordered agenda", directPDF, reordered, []string{"5. OC Streetcar"}, []string{"5. OC Streetcar"}},
		{"a tiny change inside a large agenda", large, tiny, []string{"$627,500"}, []string{"$672,500"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old, updated := normalized(tc.old), normalized(tc.new)
			if agendadiff.Equivalent(t.Context(), old, updated) {
				t.Fatal("Equivalent = true, want false")
			}
			d := compare(t, old, updated)
			if d.Equivalent() || d.Coarse {
				t.Fatalf("Compare = %+v, want precise changes", d)
			}
			var removedText, addedText strings.Builder
			for _, c := range d.Changes {
				removedText.WriteString(c.Removed + "\n")
				addedText.WriteString(c.Added + "\n")
			}
			for _, want := range tc.removed {
				if !strings.Contains(removedText.String(), want) {
					t.Errorf("removed text %q lacks %q", removedText.String(), want)
				}
			}
			for _, want := range tc.adds {
				if !strings.Contains(addedText.String(), want) {
					t.Errorf("added text %q lacks %q", addedText.String(), want)
				}
			}
			if tc.removed == nil && strings.TrimSpace(removedText.String()) != "" {
				t.Errorf("removed text = %q, want none", removedText.String())
			}
			if tc.adds == nil && strings.TrimSpace(addedText.String()) != "" {
				t.Errorf("added text = %q, want none", addedText.String())
			}
		})
	}
}

func TestATinyChangeInALargeAgendaIsOneChangeWithContext(t *testing.T) {
	large := largeAgenda(2000)
	tiny := replaceOnce(t, large, "extending the term to June 1, 2028.\n1009.", "extending the term to June 12, 2028.\n1009.")

	d := compare(t, large, tiny)
	if len(d.Changes) != 1 || d.Coarse {
		t.Fatalf("changes = %+v, want one precise change", d.Changes)
	}
	c := d.Changes[0]
	if c.Removed != "1," || c.Added != "12," {
		t.Errorf("change = %+v, want 1, to 12,", c)
	}
	if !strings.HasSuffix(c.Before, "extending the term to June") || !strings.HasPrefix(c.After, "2028. 1009.") {
		t.Errorf("context = %q / %q", c.Before, c.After)
	}
}

func TestDistantChangesStaySeparate(t *testing.T) {
	old := normalized(directPDF)
	updated := replaceOnce(t, replaceOnce(t, old, "$1,250,000", "$1,300,000"), "January 26, 2027", "February 9, 2027")

	d := compare(t, old, updated)
	if len(d.Changes) != 2 {
		t.Fatalf("changes = %+v, want two", d.Changes)
	}
	if d.Changes[0].Added != "$1,300,000," || d.Changes[1].Removed != "January 26," || d.Changes[1].Added != "February 9," {
		t.Errorf("changes = %+v", d.Changes)
	}
}

func TestAWholesaleRewriteIsCoarse(t *testing.T) {
	var a, b strings.Builder
	for i := range 3000 {
		fmt.Fprintf(&a, "alpha%d\n", i)
		fmt.Fprintf(&b, "beta%d\n", i)
	}
	d := compare(t, a.String(), b.String())
	if !d.Coarse || len(d.Changes) != 1 || !strings.HasPrefix(d.Changes[0].Removed, "alpha0 alpha1") || !strings.HasPrefix(d.Changes[0].Added, "beta0 beta1") {
		t.Errorf("diff = coarse %v, %d changes", d.Coarse, len(d.Changes))
	}
}

func TestATooLongMiddleIsCoarseWithoutDiffing(t *testing.T) {
	var old, updated strings.Builder
	updated.WriteString("start\n")
	for i := range agendadiff.MaxDiffTokens / 2 {
		fmt.Fprintf(&old, "item%d gamma\n", i)
		fmt.Fprintf(&updated, "item%d delta\n", i)
	}
	d := compare(t, old.String(), updated.String())
	if !d.Coarse || len(d.Changes) != 1 {
		t.Errorf("diff = coarse %v, %d changes, want one coarse change", d.Coarse, len(d.Changes))
	}
}

func TestEmptyAndOneSidedTexts(t *testing.T) {
	if !agendadiff.Equivalent(t.Context(), "", "Page 1 of 2\n- 2 -") {
		t.Error("page numbers alone should be equivalent to nothing")
	}
	d := compare(t, "", "1. Call to Order")
	if len(d.Changes) != 1 || d.Changes[0].Added != "1. Call to Order" || d.Changes[0].Removed != "" {
		t.Errorf("changes = %+v", d.Changes)
	}
	d = compare(t, "1. Call to Order", "")
	if len(d.Changes) != 1 || d.Changes[0].Removed != "1. Call to Order" || d.Changes[0].Added != "" {
		t.Errorf("changes = %+v", d.Changes)
	}
	if agendadiff.Equivalent(t.Context(), "Approve $12.50", "Approve $1250") {
		t.Error("a decimal point is not formatting")
	}
	if !agendadiff.Equivalent(t.Context(), "Approve $1,250", "Approve $1250") {
		t.Error("a thousands separator is formatting")
	}
}

func TestRepeatedLinesAreBoilerplateButChangedOnesAreNot(t *testing.T) {
	old := "Agenda\nRecommendation: Approve.\n1. Item one\nRecommendation: Approve.\n2. Item two"
	updated := "Agenda\nRecommendation: Approve.\n1. Item one\nRecommendation: Deny.\n2. Item two"
	if agendadiff.Equivalent(t.Context(), old, updated) {
		t.Error("a changed recommendation must not be dropped as boilerplate")
	}
}

func TestNearbyEditsMergeIntoOneChange(t *testing.T) {
	d := compare(t, "Approve the contract for $100 with Acme on May 1", "Approve the contract for $200 with Acme on June 1")
	if len(d.Changes) != 1 {
		t.Fatalf("changes = %+v, want one", d.Changes)
	}
	c := d.Changes[0]
	if c.Removed != "$100 with Acme on May" || c.Added != "$200 with Acme on June" || c.Before != "Approve the contract for" || c.After != "1" {
		t.Errorf("change = %+v", c)
	}
}

func TestContextNeverRepeatsAChangedWord(t *testing.T) {
	d := compare(t, "Item A-1 approve", "Item A-2 approve")
	if len(d.Changes) != 1 {
		t.Fatalf("changes = %+v", d.Changes)
	}
	if c := d.Changes[0]; c.Before != "Item" || c.Removed != "A-1" || c.Added != "A-2" || c.After != "approve" {
		t.Errorf("change = %+v", c)
	}
}
