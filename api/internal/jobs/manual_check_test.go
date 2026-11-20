package jobs

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/andrewcgraves/civic-scanner/api/internal/sources"
	"github.com/andrewcgraves/civic-scanner/api/internal/sources/sourcestest"
)

func TestScheduledArgsEncodeAsBeforeSoTheirUniqueKeysAreUnchanged(t *testing.T) {
	meetingID := uuid.MustParse("6f1c1b8e-8a39-4d5e-9a57-0d3c4a3e2b10")
	cases := []struct {
		args river.JobArgs
		want string
	}{
		{CheckAgencyArgs{AgencySlug: "la-metro"}, `{"agency_slug":"la-metro"}`},
		{CheckAgencyArgs{AgencySlug: "la-metro", Manual: true}, `{"agency_slug":"la-metro","manual":true}`},
		{ProcessAgendaArgs{MeetingID: meetingID, AgendaURL: "https://a.test/x.pdf"}, `{"meeting_id":"` + meetingID.String() + `","agenda_url":"https://a.test/x.pdf"}`},
		{ProcessAgendaArgs{MeetingID: meetingID, AgendaURL: "https://a.test/x.pdf", Manual: true}, `{"meeting_id":"` + meetingID.String() + `","agenda_url":"https://a.test/x.pdf","manual":true}`},
	}
	for _, tc := range cases {
		got, err := json.Marshal(tc.args)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tc.want {
			t.Errorf("%T encodes as %s, want %s", tc.args, got, tc.want)
		}
	}
}

func TestScheduledJobsStayUniquePerUTCDay(t *testing.T) {
	want := river.UniqueOpts{ByArgs: true, ByPeriod: 24 * time.Hour, ByQueue: true}
	for _, opts := range []river.InsertOpts{CheckAgencyArgs{}.InsertOpts(), ProcessAgendaArgs{}.InsertOpts()} {
		if opts.MaxAttempts != checkMaxAttempts || opts.UniqueOpts.ByPeriod != want.ByPeriod ||
			!opts.UniqueOpts.ByArgs || !opts.UniqueOpts.ByQueue || opts.UniqueOpts.ByState != nil {
			t.Errorf("scheduled insert opts = %+v, want %+v", opts, want)
		}
	}
}

func TestManualJobsAreUniqueOnlyWhileWaitingOrRunning(t *testing.T) {
	for _, opts := range []river.InsertOpts{CheckAgencyArgs{Manual: true}.InsertOpts(), ProcessAgendaArgs{Manual: true}.InsertOpts()} {
		u := opts.UniqueOpts
		if opts.MaxAttempts != checkMaxAttempts || u.ByPeriod != 0 || !u.ByArgs || !u.ByQueue {
			t.Errorf("manual insert opts = %+v", opts)
		}
		for _, s := range []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable, rivertype.JobStateRunning, rivertype.JobStateScheduled} {
			if !slices.Contains(u.ByState, s) {
				t.Errorf("manual unique states %v lack %s", u.ByState, s)
			}
		}
		for _, s := range []rivertype.JobState{rivertype.JobStateCompleted, rivertype.JobStateCancelled, rivertype.JobStateDiscarded} {
			if slices.Contains(u.ByState, s) {
				t.Errorf("manual unique states %v include %s, so a finished job would block a new manual run", u.ByState, s)
			}
		}
	}
}

func TestCheckAgencyLogsAManualTrigger(t *testing.T) {
	var buf bytes.Buffer
	w := &CheckAgencyWorker{
		logger:   slog.New(slog.NewJSONHandler(&buf, nil)),
		enabled:  flag(false),
		registry: sources.NewRegistry(sources.Deps{}, map[string]sources.Constructor{"la-metro": sourcestest.Constructor(sourcestest.Returning())}),
	}
	job := checkJob("la-metro")
	job.Args.Manual = true
	if err := w.Work(t.Context(), job); err != nil {
		t.Fatalf("Work: %v", err)
	}
	logs := buf.String()
	if !strings.Contains(logs, `"msg":"agency check triggered manually"`) || !strings.Contains(logs, `"trigger":"manual"`) {
		t.Errorf("logs lack the manual trigger:\n%s", logs)
	}

	buf.Reset()
	if err := w.Work(t.Context(), checkJob("la-metro")); err != nil {
		t.Fatalf("Work: %v", err)
	}
	if logs := buf.String(); strings.Contains(logs, "triggered manually") || !strings.Contains(logs, `"trigger":"scheduled"`) {
		t.Errorf("scheduled check logs:\n%s", logs)
	}
}
