package jobs

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const (
	refreshAgenda = "https://agency.test/board/agenda.pdf"
	refreshOther  = "https://agency.test/board/agenda-revised.pdf"
)

func TestAgendaRefreshStopsAtTheGraceAfterTheStart(t *testing.T) {
	start := time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)
	grace := 48 * time.Hour
	cutoff := start.Add(grace)
	cases := []struct {
		name    string
		now     time.Time
		agenda  string
		stored  string
		manual  bool
		settled bool
	}{
		{"upcoming", start.Add(-72 * time.Hour), refreshAgenda, refreshAgenda, false, false},
		{"at the start", start, refreshAgenda, refreshAgenda, false, false},
		{"just before the cutoff", cutoff.Add(-time.Nanosecond), refreshAgenda, refreshAgenda, false, false},
		{"at the cutoff", cutoff, refreshAgenda, refreshAgenda, false, true},
		{"after the cutoff", cutoff.Add(24 * time.Hour), refreshAgenda, refreshAgenda, false, true},
		{"30 days after", start.AddDate(0, 0, 30), refreshAgenda, refreshAgenda, false, true},
		{"past without a stored version", cutoff.Add(time.Hour), refreshAgenda, "", false, false},
		{"past with a version from another url", cutoff.Add(time.Hour), refreshAgenda, refreshOther, false, false},
		{"past without an agenda", cutoff.Add(time.Hour), "", refreshAgenda, false, false},
		{"manual after the cutoff", cutoff.Add(time.Hour), refreshAgenda, refreshAgenda, true, false},
		{"manual 30 days after", start.AddDate(0, 0, 30), refreshAgenda, refreshAgenda, true, false},
	}
	for _, c := range cases {
		r := agendaRefresh{now: c.now, grace: grace, manual: c.manual}
		if got := r.settled(start, c.agenda, c.stored); got != c.settled {
			t.Errorf("%s: settled = %v, want %v", c.name, got, c.settled)
		}
	}
}

func TestAgendaRefreshIsInAbsoluteTimeAcrossDaylightSaving(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 31, 10, 0, 0, 0, la)
	cutoff := time.Date(2026, 11, 2, 9, 0, 0, 0, la)
	if cutoff.Sub(start) != 48*time.Hour {
		t.Fatalf("cutoff is %s after the start, want 48h", cutoff.Sub(start))
	}
	for _, zone := range []*time.Location{time.UTC, la} {
		r := agendaRefresh{grace: 48 * time.Hour}
		r.now = cutoff.Add(-time.Second).In(zone)
		if r.settled(start.In(zone), refreshAgenda, refreshAgenda) {
			t.Errorf("%s: settled a second before 48 absolute hours (10:00 PDT to 09:00 PST)", zone)
		}
		r.now = cutoff.In(zone)
		if !r.settled(start.UTC(), refreshAgenda, refreshAgenda) {
			t.Errorf("%s: not settled at 48 absolute hours", zone)
		}
		r.now = time.Date(2026, 11, 2, 9, 30, 0, 0, la).In(zone)
		if !r.settled(start.In(zone), refreshAgenda, refreshAgenda) {
			t.Errorf("%s: not settled at 09:30 PST, past 48 absolute hours", zone)
		}
	}
}

func TestAgendaRefreshMode(t *testing.T) {
	if got := (agendaRefresh{}).mode(); got != "applied" {
		t.Errorf("scheduled mode = %q, want applied", got)
	}
	if got := (agendaRefresh{manual: true}).mode(); got != "bypassed" {
		t.Errorf("manual mode = %q, want bypassed", got)
	}
}

func TestEnqueueProcessAgendaSkipsSettledAgendas(t *testing.T) {
	m := UpsertedMeeting{Meeting: store.Meeting{ID: uuid.New()}, AgendaURL: refreshAgenda, AgendaSettled: true}
	if err := (EnqueueProcessAgenda{}).MeetingUpserted(t.Context(), nil, m); err != nil {
		t.Errorf("MeetingUpserted for a settled agenda = %v, want nothing done", err)
	}
}

func TestConfigDefaultsTheAgendaRefreshGrace(t *testing.T) {
	st := store.New(nil)
	if config.DefaultAgendaRefreshGrace != 48*time.Hour {
		t.Errorf("DefaultAgendaRefreshGrace = %s, want 48h", config.DefaultAgendaRefreshGrace)
	}
	if got := (Config{}).withDefaults(discard, st).AgendaGrace; got != config.DefaultAgendaRefreshGrace {
		t.Errorf("default AgendaGrace = %s, want 48h", got)
	}
	if got := (Config{AgendaGrace: 72 * time.Hour}).withDefaults(discard, st).AgendaGrace; got != 72*time.Hour {
		t.Errorf("AgendaGrace = %s, want 72h kept", got)
	}
	if got := (Config{AgendaGrace: -time.Hour}).withDefaults(discard, st).AgendaGrace; got != 48*time.Hour {
		t.Errorf("negative AgendaGrace = %s, want the 48h default", got)
	}
}
