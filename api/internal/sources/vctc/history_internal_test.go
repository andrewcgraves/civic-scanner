package vctc

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"
)

func history(t *testing.T) ([]post, Adapter) {
	t.Helper()
	body, err := os.ReadFile("testdata/vctc_history.json")
	if err != nil {
		t.Fatal(err)
	}
	var posts []post
	if err := json.Unmarshal(body, &posts); err != nil {
		t.Fatal(err)
	}
	a, err := VCTC.adapter()
	if err != nil {
		t.Fatal(err)
	}
	return posts, a
}

func TestBoardFilterAgainstEveryRecordedPost(t *testing.T) {
	posts, a := history(t)
	var kept, dropped []int
	cancelled := 0
	for _, p := range posts {
		parsed := parseTitle(p.Title.Rendered)
		_, ok := a.board(p.Types, parsed)
		agenda, hasAgenda := agendaLink(sections(p.Content.Rendered))
		if _, committee := a.committee(agenda); ok && (!hasAgenda || !committee) {
			kept = append(kept, p.ID)
			if parsed.cancelled {
				cancelled++
			}
		} else {
			dropped = append(dropped, p.ID)
		}
	}
	if len(posts) != 208 || len(kept) != 188 || cancelled != 2 {
		t.Errorf("of %d posts kept %d with %d cancelled, want 208, 188 and 2", len(posts), len(kept), cancelled)
	}
	slices.Sort(dropped)
	want := []int{1002, 1022, 1126, 1428, 1429, 1430, 1455, 1459, 1461, 1462, 1463, 1739, 1740, 1741, 1742, 1743, 1744, 1745, 1746, 1747}
	if !slices.Equal(dropped, want) {
		t.Errorf("dropped %v, want %v", dropped, want)
	}
}

func TestAgendaDatesAgainstEveryRecordedPost(t *testing.T) {
	posts, a := history(t)
	var dated []int
	for _, p := range posts {
		parsed := parseTitle(p.Title.Rendered)
		if _, ok := a.board(p.Types, parsed); !ok {
			continue
		}
		agenda, hasAgenda := agendaLink(sections(p.Content.Rendered))
		if _, committee := a.committee(agenda); hasAgenda && committee {
			continue
		}
		day, ok := a.agendaDate(agenda, hasAgenda, parsed)
		if !ok {
			continue
		}
		dated = append(dated, p.ID)
		if day.Weekday() != time.Friday {
			t.Errorf("post %d %q dated %s, a %s; the commission meets on Fridays", p.ID, parsed.full, day.Format(time.DateOnly), day.Weekday())
		}
		if m := decodeMeta(p.Meta); m.date != "" && m.date != day.Format(time.DateOnly) {
			t.Errorf("post %d dated %s from its agenda, meta says %s", p.ID, day.Format(time.DateOnly), m.date)
		}
	}
	if len(dated) != 91 {
		t.Errorf("dated %d board posts from their agenda links, want 91: %v", len(dated), dated)
	}
}

func TestParseTitle(t *testing.T) {
	cases := []struct {
		raw       string
		name      string
		cancelled bool
		precision datePrecision
	}{
		{"Commission Meeting &#8211; November 2026", "Commission Meeting", false, monthDate},
		{"Commission Meeting – December 2021 (2)", "Commission Meeting", false, monthDate},
		{"Commission Meeting – March 2015 (Special Meeting)", "Commission Meeting", false, monthDate},
		{"Commission Meeting – July 2024 (Cancelled)", "Commission Meeting", true, monthDate},
		{"Commission Meeting – Dec. 4, 2026", "Commission Meeting", false, dayDate},
		{"Commission Meeting – December 4 2026", "Commission Meeting", false, dayDate},
		{"Commission Meeting — Sept 2026", "Commission Meeting", false, monthDate},
		{"Commission Meeting – Sep. 2026", "Commission Meeting", false, monthDate},
		{"Commission Meeting – Fall 2026", "Commission Meeting", false, noDate},
		{"Commission Meeting – February 30, 2026", "Commission Meeting", false, noDate},
		{"Special Meeting VCTC Goal Setting Session", "Special Meeting VCTC Goal Setting Session", false, noDate},
		{"(Cancelled)", "(Cancelled)", false, noDate},
		{"Cancelled", "Cancelled", false, noDate},
	}
	for _, c := range cases {
		got := parseTitle(c.raw)
		if got.name != c.name || got.cancelled != c.cancelled || got.precision != c.precision {
			t.Errorf("parseTitle(%q) = %+v, want name %q cancelled %v precision %v", c.raw, got, c.name, c.cancelled, c.precision)
		}
	}
}

func TestLinkDates(t *testing.T) {
	cases := []struct {
		href string
		text string
		want []string
	}{
		{"https://x.test/AGENDA-COMMISSION-11.6.26.pdf", "Agenda", []string{"2026-11-06"}},
		{"https://x.test/VCTC-JUNE-5-2026-AGENDA.pdf", "Agenda", []string{"2026-06-05"}},
		{"https://x.test/2025.12.05_DECEMBER-5-2025-MEETING-CANCELLATION-AGENDA.pdf", "Agenda", []string{"2025-12-05"}},
		{"https://x.test/VCTC-SPECIAL-MEETING-AGENDA-08-20-2021.pdf", "Agenda", []string{"2021-08-20"}},
		{"https://x.test/Special-Meeting-Agenda-for-11.3.2023_For-Posting.pdf", "Agenda", []string{"2023-11-03"}},
		{"https://x.test/Agenda%20Sept%2011,%202026.pdf", "Agenda", []string{"2026-09-11"}},
		{"https://x.test/a.pdf", "Agenda for December 4th, 2026", []string{"2026-12-04"}},
		{"https://x.test/VCTC-JUNE-72024-AGENDA-1.pdf", "Agenda", nil},
		{"https://x.test/OCTOBER-6-AGENDA.pdf", "Agenda", nil},
		{"https://x.test/AGENDA-2.30.26.pdf", "Agenda", nil},
		{"http://[::1/AGENDA-COMMISSION-11.6.26.pdf", "Agenda", []string{"2026-11-06"}},
	}
	for _, c := range cases {
		var got []string
		for _, d := range linkDates(link{href: c.href, text: c.text}) {
			got = append(got, d.Format(time.DateOnly))
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("linkDates(%q, %q) = %v, want %v", c.href, c.text, got, c.want)
		}
	}
}
