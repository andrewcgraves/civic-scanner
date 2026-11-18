package migrations

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestUpFailsWhenDatabaseUnreachable(t *testing.T) {
	pool, err := pgxpool.New(t.Context(), "postgres://127.0.0.1:1/civic?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	err = Up(t.Context(), pool, slog.New(slog.DiscardHandler))
	if err == nil || !strings.HasPrefix(err.Error(), "acquire lock connection:") {
		t.Fatalf("err = %v, want acquire lock connection error", err)
	}
}
