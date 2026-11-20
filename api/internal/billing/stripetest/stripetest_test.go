package stripetest

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func post(t *testing.T, s *Server, path, key, auth string, form url.Values) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("Authorization", auth)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

func errorType(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	typ, _ := e["type"].(string)
	return typ
}

func TestAReusedKeyWithOtherParametersIsAnIdempotencyError(t *testing.T) {
	s := New(t)
	auth := "Bearer " + SecretKey

	status, first := post(t, s, "/v1/customers", "k1", auth, url.Values{"email": {"a@example.com"}})
	if status != http.StatusOK {
		t.Fatalf("first: %d %v", status, first)
	}
	status, again := post(t, s, "/v1/customers", "k1", auth, url.Values{"email": {"a@example.com"}})
	if status != http.StatusOK || again["id"] != first["id"] {
		t.Errorf("replay: %d %v, want %v", status, again, first["id"])
	}
	status, clash := post(t, s, "/v1/customers", "k1", auth, url.Values{"email": {"b@example.com"}})
	if status != http.StatusBadRequest || errorType(clash) != "idempotency_error" {
		t.Errorf("clash: %d %v", status, clash)
	}
	if len(s.Customers()) != 1 {
		t.Errorf("customers %v", s.Customers())
	}
}

func TestPrefixesAreUniquePerServer(t *testing.T) {
	a, b := New(t), New(t)
	auth := "Bearer " + SecretKey
	_, fromA := post(t, a, "/v1/customers", "k", auth, nil)
	_, fromB := post(t, b, "/v1/customers", "k", auth, nil)
	if fromA["id"] == fromB["id"] {
		t.Errorf("both servers issued %v", fromA["id"])
	}
}

func TestRejectsAWrongKeyAndUnknownPaths(t *testing.T) {
	s := New(t)
	if status, body := post(t, s, "/v1/customers", "k", "Bearer sk_test_other", nil); status != http.StatusUnauthorized {
		t.Errorf("wrong key: %d %v", status, body)
	}
	if status, _ := post(t, s, "/v1/unknown", "k", "Bearer "+SecretKey, nil); status != http.StatusNotFound {
		t.Errorf("unknown path: %d", status)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.URL+"/v1/customers?%zz", strings.NewReader("a=%zz"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed form: %d", resp.StatusCode)
	}
}
