//go:build integration

package httpapi_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andrewcgraves/civic-scanner/api/internal/auth"
	"github.com/andrewcgraves/civic-scanner/api/internal/billing"
	"github.com/andrewcgraves/civic-scanner/api/internal/billing/stripetest"
	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/httpapi"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

type billingServer struct {
	url    string
	token  string
	stripe *stripetest.Server
	db     *store.Store
	pool   *pgxpool.Pool
	sub    string
}

func newBillingServer(t *testing.T, billingOn bool) billingServer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwksSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
			"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(jwksSrv.Close)
	verifier, err := auth.New(config.WorkOS{
		ClientID: meClientID,
		BaseURL:  jwksSrv.URL,
		Issuers:  []string{"https://api.workos.com/user_management/" + meClientID},
	}, auth.WithHTTPClient(jwksSrv.Client()))
	if err != nil {
		t.Fatal(err)
	}

	pool := testdb.Pool(t)
	db := store.New(pool)
	fake := stripetest.New(t)
	logger := slog.New(slog.DiscardHandler)
	biller := billing.New(config.Stripe{SecretKey: stripetest.SecretKey, PriceID: "price_integration"}, "https://civic.example", db, billing.WithAPIURL(fake.URL), billing.WithoutRetryDelay())
	flags := func(_ context.Context, flag string) bool {
		return flag == httpapi.FlagAccounts || (billingOn && flag == httpapi.FlagBilling)
	}
	h := httpapi.NewHandler(db, logger, "test", httpapi.WithAuthenticator(verifier), httpapi.WithFlags(flags), httpapi.WithBilling(biller))
	srv := httptest.NewServer(httpapi.NewRouter(h, logger))
	t.Cleanup(srv.Close)

	sub := "user_billing_" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM users WHERE workos_user_id = $1`, sub)
	})
	now := time.Now()
	token := signedToken(t, key, "k1", map[string]any{
		"iss":       "https://api.workos.com/user_management/" + meClientID,
		"sub":       sub,
		"client_id": meClientID,
		"sid":       "session_billing",
		"email":     "ada@example.com",
		"iat":       now.Add(-time.Minute).Unix(),
		"auth_time": now.Add(-time.Hour).Unix(),
		"exp":       now.Add(5 * time.Minute).Unix(),
	})
	return billingServer{url: srv.URL, token: token, stripe: fake, db: db, pool: pool, sub: sub}
}

func postWithToken(t *testing.T, url, token string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	var body json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.StatusCode, resp.Header, body
}

func TestBillingAgainstPostgres(t *testing.T) {
	s := newBillingServer(t, true)

	status, _, raw := postWithToken(t, s.url+"/v1/billing/portal-session", s.token)
	if status != http.StatusConflict || !strings.Contains(string(raw), `"no_billing_customer"`) {
		t.Fatalf("portal before checkout: %d %s", status, raw)
	}

	status, header, raw := postWithToken(t, s.url+"/v1/billing/checkout-session", s.token)
	if status != http.StatusOK || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("checkout: %d %v %s", status, header, raw)
	}
	var redirect v1.BillingRedirect
	if err := json.Unmarshal(raw, &redirect); err != nil || !strings.HasPrefix(redirect.Url, "https://checkout.stripe.test/") {
		t.Fatalf("checkout body %s, err %v", raw, err)
	}

	user, _, err := s.db.EnsureUser(t.Context(), store.UserInput{WorkOSUserID: s.sub})
	if err != nil {
		t.Fatal(err)
	}
	customer, err := s.db.StripeCustomerID(t.Context(), user.ID)
	if err != nil || customer == "" || s.stripe.Customers()[customer] != user.ID.String() {
		t.Fatalf("customer %q (%v), fake %v", customer, err, s.stripe.Customers())
	}
	session := s.stripe.RequestsTo("/v1/checkout/sessions")[0]
	if session.Form.Get("client_reference_id") != user.ID.String() || session.Form.Get("success_url") != "https://civic.example/billing/success" {
		t.Errorf("session form = %v", session.Form)
	}

	status, _, raw = postWithToken(t, s.url+"/v1/billing/portal-session", s.token)
	if status != http.StatusOK || !strings.Contains(string(raw), "https://billing.stripe.test/") {
		t.Errorf("portal: %d %s", status, raw)
	}

	s.stripe.AddSubscription(customer, "active")
	status, _, raw = postWithToken(t, s.url+"/v1/billing/checkout-session", s.token)
	if status != http.StatusConflict || !strings.Contains(string(raw), `"already_subscribed"`) {
		t.Errorf("second checkout with a live subscription: %d %s", status, raw)
	}

	if status, _, _ := postWithToken(t, s.url+"/v1/billing/checkout-session", ""); status != http.StatusUnauthorized {
		t.Errorf("without a token: %d", status)
	}
}

func TestBillingFlagOffCreatesNothing(t *testing.T) {
	s := newBillingServer(t, false)
	for _, path := range []string{"/v1/billing/checkout-session", "/v1/billing/portal-session"} {
		if status, _, raw := postWithToken(t, s.url+path, s.token); status != http.StatusNotFound {
			t.Errorf("%s: %d %s", path, status, raw)
		}
	}
	if n := len(s.stripe.Requests()); n != 0 {
		t.Errorf("Stripe got %d requests", n)
	}
	var users int
	if err := s.pool.QueryRow(t.Context(), `SELECT count(*) FROM users WHERE workos_user_id = $1`, s.sub).Scan(&users); err != nil || users != 0 {
		t.Errorf("users created: %d (%v), want 0", users, err)
	}
}
