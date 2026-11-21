package jobs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/andrewcgraves/civic-scanner/api/internal/jobs"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

type recordingInserter struct {
	args []river.JobArgs
	opts []*river.InsertOpts
	err  error
}

func (r *recordingInserter) InsertTx(_ context.Context, _ pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	r.args = append(r.args, args)
	r.opts = append(r.opts, opts)
	return &rivertype.JobInsertResult{}, r.err
}

func on(context.Context, string) bool { return true }

func TestEnqueueScreenAgency(t *testing.T) {
	sub := store.AgencySubmission{AgencyID: uuid.New()}
	ins := &recordingInserter{}
	if err := (jobs.EnqueueScreenAgency{Client: ins, Enabled: on}).AgencySubmitted(t.Context(), nil, sub); err != nil {
		t.Fatal(err)
	}
	if len(ins.args) != 1 || ins.args[0] != (jobs.ScreenAgencyArgs{AgencyID: sub.AgencyID}) || ins.opts[0].Queue != river.QueueDefault {
		t.Errorf("inserted %+v with %+v", ins.args, ins.opts)
	}

	if err := (jobs.EnqueueScreenAgency{Client: ins}).AgencySubmitted(t.Context(), nil, sub); err != nil || len(ins.args) != 1 {
		t.Errorf("without a flag function: err %v, inserts %d", err, len(ins.args))
	}
	if err := (jobs.EnqueueScreenAgency{Enabled: on}).AgencySubmitted(t.Context(), nil, sub); err == nil {
		t.Error("no client: want an error")
	}
	boom := errors.New("insert failed")
	if err := (jobs.EnqueueScreenAgency{Client: &recordingInserter{err: boom}, Enabled: on}).AgencySubmitted(t.Context(), nil, sub); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the insert error", err)
	}
}

func TestScreenAgencyInsertOpts(t *testing.T) {
	opts := jobs.ScreenAgencyArgs{}.InsertOpts()
	if opts.MaxAttempts != 3 || !opts.UniqueOpts.ByArgs || len(opts.UniqueOpts.ByState) == 0 {
		t.Errorf("opts = %+v", opts)
	}
	if (jobs.ScreenAgencyArgs{}).Kind() != "screen_agency" {
		t.Error("kind changed")
	}
}
