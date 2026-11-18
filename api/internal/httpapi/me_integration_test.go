//go:build integration

package httpapi_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/auth"
	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/httpapi"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/testdb"
)

const meClientID = "client_integration"

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func signedToken(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "RS256", "typ": "JWT", "kid": kid})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	input := b64(header) + "." + b64(payload)
	digest := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + b64(sig)
}

type meServer struct {
	url   string
	key   *rsa.PrivateKey
	hits  *atomic.Int32
	users string
}

func newMeServer(t *testing.T, flagOn bool) meServer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	jwksSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
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
	logger := slog.New(slog.DiscardHandler)
	flags := func(_ context.Context, flag string) bool { return flagOn && flag == httpapi.FlagAccounts }
	h := httpapi.NewHandler(store.New(pool), logger, "test", httpapi.WithAuthenticator(verifier), httpapi.WithFlags(flags))
	srv := httptest.NewServer(httpapi.NewRouter(h, logger))
	t.Cleanup(srv.Close)

	workosID := "user_integration_" + uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM users WHERE workos_user_id = $1`, workosID)
	})
	return meServer{url: srv.URL, key: key, hits: &hits, users: workosID}
}

func (s meServer) claims(sub string, exp time.Duration) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss":       "https://api.workos.com/user_management/" + meClientID,
		"sub":       sub,
		"client_id": meClientID,
		"sid":       "session_integration",
		"email":     "ada@example.com",
		"iat":       now.Add(-time.Minute).Unix(),
		"exp":       now.Add(exp).Unix(),
	}
}

func getWithToken(t *testing.T, url, token string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
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

func fetchMe(ctx context.Context, url, token string) (v1.User, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return v1.User{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return v1.User{}, err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return v1.User{}, errors.New(resp.Status)
	}
	var u v1.User
	err = json.NewDecoder(resp.Body).Decode(&u)
	return u, err
}

func TestGetMeAgainstPostgres(t *testing.T) {
	s := newMeServer(t, true)
	token := signedToken(t, s.key, "k1", s.claims(s.users, 5*time.Minute))

	status, _, raw := getWithToken(t, s.url+"/v1/me", token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body %s", status, raw)
	}
	var first v1.User
	if err := json.Unmarshal(raw, &first); err != nil {
		t.Fatal(err)
	}
	if first.Id == uuid.Nil || first.Email == nil || *first.Email != "ada@example.com" || first.CreatedAt.IsZero() {
		t.Errorf("user = %+v", first)
	}

	_, _, raw = getWithToken(t, s.url+"/v1/me", token)
	var second v1.User
	if err := json.Unmarshal(raw, &second); err != nil {
		t.Fatal(err)
	}
	if second.Id != first.Id {
		t.Errorf("second request id = %s, want %s", second.Id, first.Id)
	}

	expired := signedToken(t, s.key, "k1", s.claims(s.users, -time.Hour))
	status, header, raw := getWithToken(t, s.url+"/v1/me", expired)
	if status != http.StatusUnauthorized || header.Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("expired token: status %d, WWW-Authenticate %q, body %s", status, header.Get("WWW-Authenticate"), raw)
	}

	if status, _, _ := getWithToken(t, s.url+"/v1/agencies", expired); status != http.StatusOK {
		t.Errorf("public endpoint with an expired token: status %d", status)
	}
	if hits := s.hits.Load(); hits != 1 {
		t.Errorf("JWKS fetches = %d, want 1", hits)
	}
}

func TestGetMeConcurrentFirstRequests(t *testing.T) {
	s := newMeServer(t, true)
	token := signedToken(t, s.key, "k1", s.claims(s.users, 5*time.Minute))

	ctx := t.Context()
	const workers = 16
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		ids = map[uuid.UUID]int{}
	)
	start := make(chan struct{})
	for range workers {
		wg.Go(func() {
			<-start
			u, err := fetchMe(ctx, s.url+"/v1/me", token)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			ids[u.Id]++
			mu.Unlock()
		})
	}
	close(start)
	wg.Wait()
	if len(ids) != 1 {
		t.Errorf("distinct user ids = %v, want one", ids)
	}
	if hits := s.hits.Load(); hits != 1 {
		t.Errorf("JWKS fetches = %d, want 1", hits)
	}
}

func TestGetMeFlagOffCreatesNoUser(t *testing.T) {
	s := newMeServer(t, false)
	token := signedToken(t, s.key, "k1", s.claims(s.users, 5*time.Minute))

	if status, _, raw := getWithToken(t, s.url+"/v1/me", token); status != http.StatusNotFound {
		t.Errorf("status = %d, body %s, want 404", status, raw)
	}
	var n int
	if err := testdb.Pool(t).QueryRow(t.Context(), `SELECT count(*) FROM users WHERE workos_user_id = $1`, s.users).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("users = %d, want 0", n)
	}
}
