package jobs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

const (
	TriggerScheduled = "scheduled"
	TriggerManual    = "manual"
)

type ManualCheck struct {
	AgencySlug string
	JobID      int64
	State      rivertype.JobState
	Duplicate  bool
}

func manualUniqueOpts() river.UniqueOpts {
	return river.UniqueOpts{
		ByArgs:  true,
		ByQueue: true,
		ByState: []rivertype.JobState{
			rivertype.JobStateAvailable,
			rivertype.JobStatePending,
			rivertype.JobStateRetryable,
			rivertype.JobStateRunning,
			rivertype.JobStateScheduled,
		},
	}
}

func trigger(manual bool) string {
	if manual {
		return TriggerManual
	}
	return TriggerScheduled
}

func EnqueueManualChecks(ctx context.Context, client *river.Client[pgx.Tx], queue string, slugs []string) ([]ManualCheck, error) {
	if len(slugs) == 0 {
		return nil, nil
	}
	params := make([]river.InsertManyParams, 0, len(slugs))
	for _, slug := range slugs {
		params = append(params, river.InsertManyParams{
			Args:       CheckAgencyArgs{AgencySlug: slug, Manual: true},
			InsertOpts: &river.InsertOpts{Queue: queue},
		})
	}
	results, err := client.InsertMany(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("enqueue manual agency checks: %w", err)
	}
	checks := make([]ManualCheck, 0, len(results))
	for _, r := range results {
		var args CheckAgencyArgs
		if err := json.Unmarshal(r.Job.EncodedArgs, &args); err != nil {
			return nil, fmt.Errorf("decode manual check job %d: %w", r.Job.ID, err)
		}
		checks = append(checks, ManualCheck{
			AgencySlug: args.AgencySlug,
			JobID:      r.Job.ID,
			State:      r.Job.State,
			Duplicate:  r.UniqueSkippedAsDuplicate,
		})
	}
	return checks, nil
}
