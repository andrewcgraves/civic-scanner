//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/auth"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

func TestAdminGateAgainstPostgres(t *testing.T) {
	ctx := t.Context()
	pool := testdb.Pool(t)
	s := store.New(pool)
	workosID := "user_admin_gate_" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE workos_user_id = $1`, workosID)
	})
	a := &fakeAuth{tokens: map[string]auth.Identity{
		"good":       {UserID: workosID, Email: workosID + "@example.com"},
		"role-claim": {UserID: workosID, Role: "admin"},
	}}
	h := NewHandler(s, slog.New(slog.DiscardHandler), "test", WithAuthenticator(a), WithFlags(flagsOn(true)))
	probe := &adminProbe{}
	gate := adminTestRouter(h, probe, testAdminRoutes)

	if rec := adminRequest(t, gate, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", rec.Code)
	}
	if rec := adminRequest(t, gate, "Bearer good"); rec.Code != http.StatusForbidden {
		t.Fatalf("first sight: status = %d, want 403", rec.Code)
	}
	if rec := adminRequest(t, gate, "Bearer role-claim"); rec.Code != http.StatusForbidden {
		t.Fatalf("user whose token claims admin: status = %d, want 403", rec.Code)
	}

	rec := request(t, h, "/v1/me", "Bearer good")
	var me v1.User
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil || rec.Code != http.StatusOK || me.Role != v1.UserRoleUser {
		t.Fatalf("GET /v1/me = %d %s, want 200 with role user", rec.Code, rec.Body)
	}

	if _, err := s.SetUserRole(ctx, store.UserRef{ID: me.Id}, store.RoleAdmin, time.Second); err != nil {
		t.Fatal(err)
	}
	if rec := adminRequest(t, gate, "Bearer good"); rec.Code != http.StatusOK || !probe.ok || probe.admin.ID != me.Id {
		t.Fatalf("after promotion: status = %d, admin %+v, want 200 as %s", rec.Code, probe.admin, me.Id)
	}
	rec = request(t, h, "/v1/me", "Bearer good")
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil || me.Role != v1.UserRoleAdmin {
		t.Fatalf("GET /v1/me after promotion = %s, want role admin", rec.Body)
	}

	newEmail := "renamed_" + workosID + "@example.com"
	a.tokens["good"] = auth.Identity{UserID: workosID, Email: newEmail}
	if rec := adminRequest(t, gate, "Bearer good"); rec.Code != http.StatusOK || probe.admin.Email == nil || *probe.admin.Email != newEmail || probe.admin.Role != store.RoleAdmin {
		t.Fatalf("after an email change: status = %d, admin %+v, want 200 as the same admin with the new email", rec.Code, probe.admin)
	}

	if _, err := s.SetUserRole(ctx, store.UserRef{ID: me.Id}, store.RoleUser, time.Second); err != nil {
		t.Fatal(err)
	}
	if rec := adminRequest(t, gate, "Bearer good"); rec.Code != http.StatusForbidden {
		t.Fatalf("after demotion: status = %d, want 403", rec.Code)
	}
	if probe.calls != 2 {
		t.Errorf("handler ran %d times, want twice", probe.calls)
	}
}
