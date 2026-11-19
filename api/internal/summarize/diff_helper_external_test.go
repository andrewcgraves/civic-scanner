package summarize_test

import (
	"testing"

	"github.com/andrewcgraves/civic-scanner/api/internal/agendadiff"
)

func diffOf(t *testing.T, old, updated string) agendadiff.Diff {
	t.Helper()
	d, err := agendadiff.Compare(t.Context(), old, updated)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
