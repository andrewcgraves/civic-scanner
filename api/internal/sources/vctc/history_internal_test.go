package vctc

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

func TestBoardFilterAgainstEveryRecordedTitle(t *testing.T) {
	body, err := os.ReadFile("testdata/vctc_history_titles.json")
	if err != nil {
		t.Fatal(err)
	}
	var history []post
	if err := json.Unmarshal(body, &history); err != nil {
		t.Fatal(err)
	}
	a, err := VCTC.adapter()
	if err != nil {
		t.Fatal(err)
	}
	var kept, dropped []int
	cancelled := 0
	for _, p := range history {
		parsed := parseTitle(p.Title.Rendered)
		if _, ok := a.board(p.Types, parsed); ok {
			kept = append(kept, p.ID)
			if parsed.cancelled {
				cancelled++
			}
		} else {
			dropped = append(dropped, p.ID)
		}
	}
	if len(history) != 208 || len(kept) != 189 || cancelled != 2 {
		t.Errorf("of %d posts kept %d with %d cancelled, want 208, 189 and 2", len(history), len(kept), cancelled)
	}
	slices.Sort(dropped)
	want := []int{1002, 1022, 1428, 1429, 1430, 1455, 1459, 1461, 1462, 1463, 1739, 1740, 1741, 1742, 1743, 1744, 1745, 1746, 1747}
	if !slices.Equal(dropped, want) {
		t.Errorf("dropped %v, want %v", dropped, want)
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
		{"Commission Meeting — Sept 2026", "Commission Meeting", false, noDate},
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
