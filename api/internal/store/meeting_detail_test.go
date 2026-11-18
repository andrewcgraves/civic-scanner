package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var errBoom = errors.New("boom")

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

type okRow struct{}

func (okRow) Scan(...any) error { return nil }

type failingRows struct {
	pgx.Rows
	next    bool
	scanErr error
	err     error
}

func (r *failingRows) Next() bool {
	next := r.next
	r.next = false
	return next
}

func (r *failingRows) Scan(...any) error { return r.scanErr }
func (r *failingRows) Err() error        { return r.err }
func (r *failingRows) Close()            {}

type scriptedQuerier struct {
	rows     []pgx.Row
	queryErr error
	result   pgx.Rows
}

func (q *scriptedQuerier) QueryRow(context.Context, string, ...any) pgx.Row {
	row := q.rows[0]
	q.rows = q.rows[1:]
	return row
}

func (q *scriptedQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return q.result, q.queryErr
}

func TestGetLiveMeetingDetailWrapsEachQueryError(t *testing.T) {
	for name, tc := range map[string]struct {
		q    *scriptedQuerier
		want string
	}{
		"meeting":         {&scriptedQuerier{rows: []pgx.Row{errRow{errBoom}}}, "get meeting: boom"},
		"agenda":          {&scriptedQuerier{rows: []pgx.Row{okRow{}, errRow{errBoom}}}, "get latest agenda version: boom"},
		"recordings":      {&scriptedQuerier{rows: []pgx.Row{okRow{}, errRow{pgx.ErrNoRows}}, queryErr: errBoom}, "query recordings: boom"},
		"recordings rows": {&scriptedQuerier{rows: []pgx.Row{okRow{}, errRow{pgx.ErrNoRows}}, result: &failingRows{err: errBoom}}, "scan recordings: boom"},
		"recording scan":  {&scriptedQuerier{rows: []pgx.Row{okRow{}, errRow{pgx.ErrNoRows}}, result: &failingRows{next: true, scanErr: errBoom}}, "scan recordings: boom"},
	} {
		_, err := getLiveMeetingDetail(t.Context(), tc.q, uuid.New())
		if !errors.Is(err, errBoom) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q wrapping boom", name, err, tc.want)
		}
	}
}
