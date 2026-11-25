package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"
	"github.com/open-feature/go-sdk/openfeature"

	"github.com/andrewcgraves/civic-scanner/api/internal/auth"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const (
	testPaidOperation       = "TestPaidOperation"
	paymentRequiredResponse = "#/components/responses/PaymentRequired"
	paidExtension           = "x-paid"
)

type paidProbe struct {
	calls int
	user  store.User
	ok    bool
}

func (p *paidProbe) handler(ctx context.Context, w http.ResponseWriter, _ *http.Request, _ any) (any, error) {
	p.calls++
	p.user, p.ok = paidFrom(ctx)
	w.WriteHeader(http.StatusNoContent)
	return nil, nil
}

func runPaidGate(t *testing.T, h *Handler, probe *paidProbe, operationID string, authErr error) *httptest.ResponseRecorder {
	t.Helper()
	ctx := context.WithValue(t.Context(), authContextKey{}, authResult{identity: auth.Identity{UserID: "user_01"}, err: authErr})
	gated := h.paidGate(map[string]string{testPaidOperation: ""})(probe.handler, operationID)
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/v1/test-paid", nil)
	if _, err := gated(ctx, rec, req, nil); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestPaidGateLetsAPaidUserThrough(t *testing.T) {
	user := store.User{ID: uuid.New(), Plan: store.PlanPaid}
	calls := &userCalls{user: user}
	h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithFlags(flagsOn(true)))
	probe := &paidProbe{}

	rec := runPaidGate(t, h, probe, testPaidOperation, nil)
	if rec.Code != http.StatusNoContent || probe.calls != 1 {
		t.Fatalf("status %d, calls %d", rec.Code, probe.calls)
	}
	if !probe.ok || probe.user.ID != user.ID {
		t.Errorf("paidFrom = (%+v, %v), want the user", probe.user, probe.ok)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
}

func TestPaidGateLetsAGrantedFreeUserThrough(t *testing.T) {
	for _, tc := range []struct {
		name    string
		feature string
		flags   []string
	}{
		{"webhook route", FlagWebhookSubscriptions, []string{FlagAccounts, FlagWebhookSubscriptions, FlagPlanGranted}},
		{"route without a flag of its own", "", []string{FlagAccounts, FlagPlanGranted}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user := store.User{ID: uuid.New(), Plan: store.PlanFree}
			h := NewHandler(fakeStore{users: &userCalls{user: user}}, slog.New(slog.DiscardHandler), "test", WithFlags(webhookFlags(tc.flags...)))
			probe := &paidProbe{}
			rec := runGatedOperation(t, h, probe, tc.feature)
			if rec.Code != http.StatusNoContent || probe.calls != 1 || !probe.ok || probe.user.ID != user.ID {
				t.Fatalf("status %d, calls %d, paidFrom = (%+v, %v)", rec.Code, probe.calls, probe.user, probe.ok)
			}
		})
	}
}

func TestPaidGateRefusesAFreeUserWithoutTheGrant(t *testing.T) {
	h := NewHandler(fakeStore{users: &userCalls{user: store.User{ID: uuid.New(), Plan: store.PlanFree}}}, slog.New(slog.DiscardHandler), "test",
		WithFlags(webhookFlags(FlagAccounts, FlagWebhookSubscriptions)))
	probe := &paidProbe{}
	rec := runGatedOperation(t, h, probe, FlagWebhookSubscriptions)
	if rec.Code != http.StatusPaymentRequired || probe.calls != 0 {
		t.Errorf("status %d, calls %d, want 402 and no call", rec.Code, probe.calls)
	}
}

func TestPaidGateGrantDoesNotSkipTheFeatureFlag(t *testing.T) {
	h := NewHandler(fakeStore{users: &userCalls{user: store.User{ID: uuid.New(), Plan: store.PlanFree}}}, slog.New(slog.DiscardHandler), "test",
		WithFlags(webhookFlags(FlagAccounts, FlagPlanGranted)))
	probe := &paidProbe{}
	rec := runGatedOperation(t, h, probe, FlagWebhookSubscriptions)
	if rec.Code != http.StatusNotFound || probe.calls != 0 {
		t.Errorf("status %d, calls %d, want 404 and no call", rec.Code, probe.calls)
	}
}

func runGatedOperation(t *testing.T, h *Handler, probe *paidProbe, feature string) *httptest.ResponseRecorder {
	t.Helper()
	ctx := context.WithValue(t.Context(), authContextKey{}, authResult{identity: auth.Identity{UserID: "user_01"}})
	gated := h.paidGate(map[string]string{testPaidOperation: feature})(probe.handler, testPaidOperation)
	rec := httptest.NewRecorder()
	if _, err := gated(ctx, rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/v1/test-paid", nil), nil); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestPaidGateRefuses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		authErr error
		flags   bool
		user    store.User
		userErr error
		status  int
		code    string
	}{
		{"no token", ErrNoCredentials, true, store.User{}, nil, http.StatusUnauthorized, "unauthorized"},
		{"bad token", auth.ErrInvalidToken, true, store.User{}, nil, http.StatusUnauthorized, "unauthorized"},
		{"auth not configured", auth.ErrNotConfigured, true, store.User{}, nil, http.StatusServiceUnavailable, "auth_not_configured"},
		{"auth unavailable", auth.ErrUnavailable, true, store.User{}, nil, http.StatusServiceUnavailable, "auth_unavailable"},
		{"accounts off", nil, false, store.User{Plan: store.PlanPaid}, nil, http.StatusNotFound, "not_found"},
		{"free", nil, true, store.User{ID: uuid.New(), Plan: store.PlanFree}, nil, http.StatusPaymentRequired, "payment_required"},
		{"empty plan", nil, true, store.User{ID: uuid.New()}, nil, http.StatusPaymentRequired, "payment_required"},
		{"unknown plan", nil, true, store.User{ID: uuid.New(), Plan: "Paid"}, nil, http.StatusPaymentRequired, "payment_required"},
		{"database error", nil, true, store.User{}, errors.New("database down"), http.StatusInternalServerError, "internal_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := &userCalls{user: tc.user, err: tc.userErr}
			h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithFlags(flagsOn(tc.flags)))
			probe := &paidProbe{}
			rec := runPaidGate(t, h, probe, testPaidOperation, tc.authErr)
			if rec.Code != tc.status || decodeError(t, rec).Code != tc.code {
				t.Errorf("status %d, body %s, want %d %s", rec.Code, rec.Body, tc.status, tc.code)
			}
			if probe.calls != 0 {
				t.Error("the handler ran")
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q", got)
			}
			if wantAuth := tc.status == http.StatusUnauthorized; (rec.Header().Get("WWW-Authenticate") == "Bearer") != wantAuth {
				t.Errorf("WWW-Authenticate = %q", rec.Header().Get("WWW-Authenticate"))
			}
			if tc.authErr != nil || !tc.flags {
				if len(calls.inputs) != 0 {
					t.Error("a user was loaded before the token and flag checks passed")
				}
			}
		})
	}
}

func TestPaidGatePassesOtherOperationsThrough(t *testing.T) {
	h := NewHandler(fakeStore{}, slog.New(slog.DiscardHandler), "test")
	probe := &paidProbe{}
	rec := runPaidGate(t, h, probe, "GetMe", ErrNoCredentials)
	if rec.Code != http.StatusNoContent || probe.calls != 1 || probe.ok {
		t.Errorf("status %d, calls %d, paid %v; want the handler to run ungated", rec.Code, probe.calls, probe.ok)
	}
	if rec.Header().Get("Cache-Control") != "" {
		t.Error("an ungated operation got Cache-Control from the paid gate")
	}
}

func TestPaidFromFailsClosed(t *testing.T) {
	ctx := context.Background()
	if _, ok := paidFrom(ctx); ok {
		t.Error("a bare context reported a paid user")
	}
	if _, ok := paidFrom(context.WithValue(ctx, paidContextKey{}, store.User{Plan: store.PlanPaid})); ok {
		t.Error("paidFrom accepted a user the gate did not let through")
	}
	if _, ok := paidFrom(context.WithValue(ctx, paidContextKey{}, "paid")); ok {
		t.Error("paidFrom accepted a value that is not a user")
	}
}

func TestRouterRunsThePaidGate(t *testing.T) {
	paidOperations["GetMe"] = ""
	t.Cleanup(func() { delete(paidOperations, "GetMe") })

	calls := &userCalls{user: store.User{ID: uuid.New(), Plan: store.PlanFree}}
	h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(goodAuth()), WithFlags(flagsOn(true)))

	if rec := request(t, h, "/v1/me", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", rec.Code)
	}
	rec := request(t, h, "/v1/me", "Bearer good")
	if rec.Code != http.StatusPaymentRequired || decodeError(t, rec).Code != "payment_required" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("free: status = %d, body %s, want 402 payment_required", rec.Code, rec.Body)
	}
	calls.user.Plan = store.PlanPaid
	rec = request(t, h, "/v1/me", "Bearer good")
	var me v1.User
	if err := json.Unmarshal(rec.Body.Bytes(), &me); rec.Code != http.StatusOK || err != nil || me.Plan != v1.Paid {
		t.Errorf("paid: status = %d, body %s, want 200 with plan paid", rec.Code, rec.Body)
	}
}

func TestGetMePlan(t *testing.T) {
	for stored, want := range map[store.Plan]v1.UserPlan{
		store.PlanFree: v1.Free,
		store.PlanPaid: v1.Paid,
		"":             v1.Free,
		"enterprise":   v1.Free,
	} {
		calls := &userCalls{user: store.User{ID: uuid.New(), Plan: stored}}
		h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(goodAuth()), WithFlags(flagsOn(true)))
		rec := request(t, h, "/v1/me", "Bearer good")
		var me v1.User
		if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("%q: status %d, body %s, err %v", stored, rec.Code, rec.Body, err)
		}
		if me.Plan != want || me.PlanGranted {
			t.Errorf("stored %q: plan = %q, planGranted %v, want %q and false", stored, me.Plan, me.PlanGranted, want)
		}
	}
}

func TestGetMePlanWithTheGrant(t *testing.T) {
	for _, tc := range []struct {
		stored  store.Plan
		granted bool
	}{
		{store.PlanFree, true},
		{"", true},
		{store.PlanPaid, false},
	} {
		calls := &userCalls{user: store.User{ID: uuid.New(), Plan: tc.stored}}
		h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(goodAuth()),
			WithFlags(webhookFlags(FlagAccounts, FlagPlanGranted)))
		rec := request(t, h, "/v1/me", "Bearer good")
		var me v1.User
		if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("%q: status %d, body %s, err %v", tc.stored, rec.Code, rec.Body, err)
		}
		if me.Plan != v1.Paid || me.PlanGranted != tc.granted {
			t.Errorf("stored %q: plan = %q, planGranted %v, want paid and %v", tc.stored, me.Plan, me.PlanGranted, tc.granted)
		}
	}
}

func TestGetMeChecksTheGrantForTheSignedInUser(t *testing.T) {
	calls := &userCalls{user: store.User{ID: uuid.New(), Plan: store.PlanFree}}
	var asked []string
	h := NewHandler(fakeStore{users: calls}, slog.New(slog.DiscardHandler), "test", WithAuthenticator(goodAuth()),
		WithFlags(func(ctx context.Context, flag string) bool {
			if flag == FlagPlanGranted {
				asked = append(asked, openfeature.TransactionContext(ctx).TargetingKey())
				return false
			}
			return flag == FlagAccounts
		}))
	rec := request(t, h, "/v1/me", "Bearer good")
	var me v1.User
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil || rec.Code != http.StatusOK || me.Plan != v1.Free || me.PlanGranted {
		t.Fatalf("status %d, body %s, want 200 with plan free", rec.Code, rec.Body)
	}
	if len(asked) != 1 || asked[0] != "user_01" {
		t.Errorf("grant checked for %v, want the signed-in user once", asked)
	}
}

func paidSpecProblems(doc *openapi3.T, operations map[string]string) []string {
	var problems []string
	marked := map[string]bool{}
	known := map[string]bool{}
	for _, item := range doc.Paths.Map() {
		for _, op := range item.Operations() {
			id := goOperationID(op.OperationID)
			known[id] = true
			flag, hasFlag := op.Extensions[paidExtension]
			isPaid := flag == true
			if hasFlag && !isPaid {
				problems = append(problems, fmt.Sprintf("%s: %s must be true or absent, got %v", id, paidExtension, flag))
			}
			requires := false
			for _, response := range op.Responses.Map() {
				if response.Ref == paymentRequiredResponse {
					requires = true
				}
			}
			switch {
			case requires && !isPaid:
				problems = append(problems, fmt.Sprintf("%s: can answer %s but is not marked %s", id, paymentRequiredResponse, paidExtension))
			case isPaid && !requires:
				problems = append(problems, fmt.Sprintf("%s: is marked %s but does not answer %s", id, paidExtension, paymentRequiredResponse))
			}
			if isPaid {
				marked[id] = true
				if _, ok := operations[id]; !ok {
					problems = append(problems, fmt.Sprintf("%s: marked %s in the spec but missing from paidOperations", id, paidExtension))
				}
			}
		}
	}
	for id := range operations {
		switch {
		case !known[id]:
			problems = append(problems, fmt.Sprintf("%s: in paidOperations but not an operation in the spec", id))
		case !marked[id]:
			problems = append(problems, fmt.Sprintf("%s: in paidOperations but not marked %s in the spec", id, paidExtension))
		}
	}
	slices.Sort(problems)
	return problems
}

func TestPaidOperationsMatchTheSpec(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	if problems := paidSpecProblems(doc, paidOperations); len(problems) > 0 {
		t.Errorf("paid operations disagree with %s:\n%s", specPath, strings.Join(problems, "\n"))
	}
	if doc.Components.Responses["PaymentRequired"] == nil {
		t.Error("the spec has no PaymentRequired response for paid operations to use")
	}
}

const paidSpecFixture = `openapi: 3.0.3
info: {title: t, version: "1"}
paths:
  /alerts:
    post:
      operationId: createAlert
      x-paid: true
      responses:
        "402": {$ref: "#/components/responses/PaymentRequired"}
  /forgotten:
    get:
      operationId: listForgotten
      responses:
        "402": {$ref: "#/components/responses/PaymentRequired"}
  /unrequired:
    get:
      operationId: listUnrequired
      x-paid: true
      responses:
        "200": {description: ok}
  /stringly:
    get:
      operationId: listStringly
      x-paid: "yes"
      responses:
        "402": {$ref: "#/components/responses/PaymentRequired"}
  /public:
    get:
      operationId: getPublic
      responses:
        "200": {description: ok}
components:
  responses:
    PaymentRequired: {description: pay}
`

func TestPaidSpecProblemsCatchesMistakes(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromData([]byte(paidSpecFixture))
	if err != nil {
		t.Fatal(err)
	}
	got := paidSpecProblems(doc, map[string]string{"CreateAlert": "", "ListUnrequired": "", "GetPublic": "", "CreatAlert": ""})
	want := []string{
		"CreatAlert: in paidOperations but not an operation in the spec",
		"GetPublic: in paidOperations but not marked x-paid in the spec",
		"ListForgotten: can answer #/components/responses/PaymentRequired but is not marked x-paid",
		"ListStringly: can answer #/components/responses/PaymentRequired but is not marked x-paid",
		"ListStringly: x-paid must be true or absent, got yes",
		"ListUnrequired: is marked x-paid but does not answer #/components/responses/PaymentRequired",
	}
	if !slices.Equal(got, want) {
		t.Errorf("problems:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if missing := paidSpecProblems(doc, map[string]string{}); !slices.Contains(missing, "CreateAlert: marked x-paid in the spec but missing from paidOperations") {
		t.Errorf("missing entry not reported: %v", missing)
	}
}
