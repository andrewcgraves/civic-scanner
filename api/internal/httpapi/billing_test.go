package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/auth"
	"github.com/andrewcgraves/civic-scanner/api/internal/billing"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const (
	checkoutPath = "/v1/billing/checkout-session"
	portalPath   = "/v1/billing/portal-session"
)

type fakeBiller struct {
	unconfigured bool
	url          string
	err          error
	users        []store.User
}

func (f *fakeBiller) Configured() bool { return !f.unconfigured }

func (f *fakeBiller) StartCheckout(_ context.Context, user store.User) (string, error) {
	f.users = append(f.users, user)
	return f.url, f.err
}

func (f *fakeBiller) OpenPortal(_ context.Context, user store.User) (string, error) {
	f.users = append(f.users, user)
	return f.url, f.err
}

func billingFlags(accounts, billingOn bool) func(context.Context, string) bool {
	return func(_ context.Context, flag string) bool {
		return (flag == FlagAccounts && accounts) || (flag == FlagBilling && billingOn)
	}
}

func post(t *testing.T, h *Handler, path, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	router := NewRouter(h, slog.New(slog.DiscardHandler))
	req := httptest.NewRequest(http.MethodPost, path, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestBillingRedirects(t *testing.T) {
	for _, path := range []string{checkoutPath, portalPath} {
		user := store.User{ID: uuid.New(), WorkOSUserID: "user_01"}
		calls := &userCalls{user: user}
		b := &fakeBiller{url: "https://checkout.stripe.test/c/pay/cs_1"}
		h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(goodAuth()), WithFlags(billingFlags(true, true)), WithBilling(b))

		rec := post(t, h, path, "Bearer good")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d, body %s", path, rec.Code, rec.Body)
		}
		var body v1.BillingRedirect
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Url != b.url {
			t.Errorf("%s: body %s, err %v", path, rec.Body, err)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q", path, got)
		}
		if len(b.users) != 1 || b.users[0].ID != user.ID {
			t.Errorf("%s: biller got %+v", path, b.users)
		}
		if len(calls.inputs) != 1 || calls.inputs[0].WorkOSUserID != "user_01" {
			t.Errorf("%s: EnsureUser inputs %+v", path, calls.inputs)
		}
	}
}

func TestBillingChecksTokenThenFlagsThenConfiguration(t *testing.T) {
	tests := []struct {
		name          string
		auth          *fakeAuth
		authorization string
		accounts      bool
		billingOn     bool
		unconfigured  bool
		wantStatus    int
		wantCode      string
	}{
		{"no token", goodAuth(), "", true, true, false, http.StatusUnauthorized, "unauthorized"},
		{"invalid token", goodAuth(), "Bearer bad", true, true, false, http.StatusUnauthorized, "unauthorized"},
		{"no token, flags off", goodAuth(), "", false, false, true, http.StatusUnauthorized, "unauthorized"},
		{"auth not configured", &fakeAuth{unconfigured: true}, "Bearer good", true, true, false, http.StatusServiceUnavailable, "auth_not_configured"},
		{"keys unavailable", &fakeAuth{err: fmt.Errorf("%w: down", auth.ErrUnavailable)}, "Bearer good", false, false, true, http.StatusServiceUnavailable, "auth_unavailable"},
		{"billing flag off", goodAuth(), "Bearer good", true, false, false, http.StatusNotFound, "not_found"},
		{"accounts flag off", goodAuth(), "Bearer good", false, true, false, http.StatusNotFound, "not_found"},
		{"flag off and not configured", goodAuth(), "Bearer good", true, false, true, http.StatusNotFound, "not_found"},
		{"not configured", goodAuth(), "Bearer good", true, true, true, http.StatusServiceUnavailable, "billing_not_configured"},
	}
	for _, path := range []string{checkoutPath, portalPath} {
		for _, tt := range tests {
			t.Run(path+" "+tt.name, func(t *testing.T) {
				calls := &userCalls{}
				b := &fakeBiller{unconfigured: tt.unconfigured}
				h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(tt.auth), WithFlags(billingFlags(tt.accounts, tt.billingOn)), WithBilling(b))
				rec := post(t, h, path, tt.authorization)
				if rec.Code != tt.wantStatus || decodeError(t, rec).Code != tt.wantCode {
					t.Errorf("status %d, body %s, want %d %s", rec.Code, rec.Body, tt.wantStatus, tt.wantCode)
				}
				if got := rec.Header().Get("Cache-Control"); got != "no-store" {
					t.Errorf("Cache-Control = %q, want no-store", got)
				}
				if tt.wantStatus == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") != "Bearer" {
					t.Errorf("WWW-Authenticate = %q", rec.Header().Get("WWW-Authenticate"))
				}
				if len(calls.inputs) != 0 || len(b.users) != 0 {
					t.Errorf("EnsureUser %+v, biller %+v, want neither called", calls.inputs, b.users)
				}
			})
		}
	}
}

func TestBillingDefaultsToNotConfigured(t *testing.T) {
	calls := &userCalls{}
	h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(goodAuth()), WithFlags(billingFlags(true, true)))
	for _, path := range []string{checkoutPath, portalPath} {
		rec := post(t, h, path, "Bearer good")
		if rec.Code != http.StatusServiceUnavailable || decodeError(t, rec).Code != "billing_not_configured" {
			t.Errorf("%s: status %d, body %s", path, rec.Code, rec.Body)
		}
	}
}

func TestBillingMapsErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		storeErr   error
		wantStatus int
		wantCode   string
	}{
		{"already subscribed", billing.ErrAlreadySubscribed, nil, http.StatusConflict, "already_subscribed"},
		{"no customer", billing.ErrNoCustomer, nil, http.StatusConflict, "no_billing_customer"},
		{"stripe failed", fmt.Errorf("create checkout session: %w", billing.ErrUnavailable), nil, http.StatusBadGateway, "billing_unavailable"},
		{"not configured late", billing.ErrNotConfigured, nil, http.StatusServiceUnavailable, "billing_not_configured"},
		{"database", errors.New("connection refused"), nil, http.StatusInternalServerError, "internal_error"},
		{"user store", nil, errors.New("connection refused"), http.StatusInternalServerError, "internal_error"},
	}
	for _, path := range []string{checkoutPath, portalPath} {
		for _, tt := range tests {
			t.Run(path+" "+tt.name, func(t *testing.T) {
				b := &fakeBiller{url: "https://ignored.example", err: tt.err}
				h := NewHandler(fakeStore{users: &userCalls{user: store.User{ID: uuid.New()}, err: tt.storeErr}}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(goodAuth()), WithFlags(billingFlags(true, true)), WithBilling(b))
				rec := post(t, h, path, "Bearer good")
				body := decodeError(t, rec)
				if rec.Code != tt.wantStatus || body.Code != tt.wantCode || body.Message == "" {
					t.Errorf("status %d, body %s, want %d %s", rec.Code, rec.Body, tt.wantStatus, tt.wantCode)
				}
				if got := rec.Header().Get("Cache-Control"); got != "no-store" {
					t.Errorf("Cache-Control = %q, want no-store", got)
				}
			})
		}
	}
}

func TestBillingOperationsAreNotAdminOnly(t *testing.T) {
	for _, op := range []string{"CreateCheckoutSession", "CreateBillingPortalSession"} {
		if _, ok := adminOperations[op]; ok {
			t.Errorf("%s is admin-only", op)
		}
		if !privateOperations[op] {
			t.Errorf("%s is not in privateOperations", op)
		}
	}
}

func TestBillingRoutesArePostOnly(t *testing.T) {
	h := NewHandler(fakeStore{}, slog.New(slog.DiscardHandler), "test", WithBilling(&fakeBiller{}))
	for _, path := range []string{checkoutPath, portalPath} {
		rec := request(t, h, path, "Bearer good")
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" {
			t.Errorf("GET %s: status %d, Allow %q", path, rec.Code, rec.Header().Get("Allow"))
		}
	}
}
