package stripetest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

const SecretKey = "sk_test_stripetest" //nolint:gosec

type Request struct {
	Method         string
	Path           string
	Form           url.Values
	IdempotencyKey string
	StripeVersion  string
	Authorization  string
}

type Failure struct {
	Status int
	Type   string
	Code   string
	Param  string
}

type Subscription struct {
	Customer string
	Status   string
}

type Server struct {
	URL string

	mu               sync.Mutex
	prefix           string
	requests         []Request
	customers        map[string]string
	byKey            map[string]keyed
	inflight         map[string]bool
	subscriptions    []Subscription
	failures         map[string][]Failure
	hang             map[string]bool
	ignoreKeys       bool
	rejectConcurrent bool
	hold             chan struct{}
	nextID           int
	sessionURL       *string
	customerNoID     bool
	customerHook     func()
}

type keyed struct {
	id     string
	params string
}

func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{
		prefix:    strings.ReplaceAll(uuid.NewString()[:8], "-", ""),
		customers: map[string]string{},
		byKey:     map[string]keyed{},
		inflight:  map[string]bool{},
		failures:  map[string][]Failure{},
		hang:      map[string]bool{},
	}
	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

func (s *Server) FailNext(path string, failures ...Failure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[path] = append(s.failures[path], failures...)
}

func (s *Server) Hang(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hang[path] = true
}

func (s *Server) IgnoreIdempotencyKeys() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ignoreKeys = true
}

func (s *Server) RejectConcurrentKeys() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejectConcurrent = true
}

func (s *Server) HoldCustomerCreates() func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	hold := make(chan struct{})
	s.hold = hold
	var once sync.Once
	return func() { once.Do(func() { close(hold) }) }
}

func (s *Server) ReturnSessionURL(link string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionURL = &link
}

func (s *Server) ReturnCustomerWithoutID() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.customerNoID = true
}

func (s *Server) OnCustomerCreate(hook func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.customerHook = hook
}

func (s *Server) AddSubscription(customer, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscriptions = append(s.subscriptions, Subscription{Customer: customer, Status: status})
}

func (s *Server) AddCustomer(id, userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.customers[id] = userID
}

func (s *Server) DeleteCustomer(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.customers, id)
}

func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

func (s *Server) RequestsTo(path string) []Request {
	var out []Request
	for _, r := range s.Requests() {
		if r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

func (s *Server) Customers() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.customers))
	for id, user := range s.customers {
		out[id] = user
	}
	return out
}

func Barrier(n int) func() {
	var mu sync.Mutex
	arrived := 0
	all := make(chan struct{})
	return func() {
		mu.Lock()
		arrived++
		if arrived == n {
			close(all)
		}
		mu.Unlock()
		<-all
	}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeError(w, Failure{Status: http.StatusBadRequest, Type: "invalid_request_error"})
		return
	}
	form := r.PostForm
	if r.Method == http.MethodGet {
		form = r.URL.Query()
	}
	req := Request{
		Method:         r.Method,
		Path:           r.URL.Path,
		Form:           form,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
		StripeVersion:  r.Header.Get("Stripe-Version"),
		Authorization:  r.Header.Get("Authorization"),
	}

	s.mu.Lock()
	s.requests = append(s.requests, req)
	hang := s.hang[req.Path]
	var failure *Failure
	if queued := s.failures[req.Path]; len(queued) > 0 {
		failure = &queued[0]
		s.failures[req.Path] = queued[1:]
	}
	s.mu.Unlock()

	switch {
	case hang:
		<-r.Context().Done()
		return
	case req.Authorization != "Bearer "+SecretKey:
		writeError(w, Failure{Status: http.StatusUnauthorized, Type: "invalid_request_error"})
		return
	case failure != nil:
		writeError(w, *failure)
		return
	}

	switch {
	case r.Method == http.MethodPost && req.Path == "/v1/customers":
		s.createCustomer(w, req)
	case r.Method == http.MethodGet && req.Path == "/v1/subscriptions":
		s.listSubscriptions(w, req)
	case r.Method == http.MethodPost && req.Path == "/v1/checkout/sessions":
		s.createSession(w, req, "cs_test_", "https://checkout.stripe.test/c/pay/")
	case r.Method == http.MethodPost && req.Path == "/v1/billing_portal/sessions":
		s.createSession(w, req, "bps_test_", "https://billing.stripe.test/p/session/")
	default:
		writeError(w, Failure{Status: http.StatusNotFound, Type: "invalid_request_error"})
	}
}

func (s *Server) createCustomer(w http.ResponseWriter, req Request) {
	key := req.IdempotencyKey
	s.mu.Lock()
	if s.rejectConcurrent && s.inflight[key] {
		s.mu.Unlock()
		writeError(w, Failure{Status: http.StatusConflict, Type: "idempotency_error"})
		return
	}
	s.inflight[key] = true
	hook, hold := s.customerHook, s.hold
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.inflight, key)
		s.mu.Unlock()
	}()
	if hook != nil {
		hook()
	}
	if hold != nil {
		<-hold
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.customerNoID {
		writeJSON(w, map[string]any{"object": "customer"})
		return
	}
	params := req.Form.Encode()
	if prior, ok := s.byKey[key]; ok && !s.ignoreKeys {
		if prior.params != params {
			writeError(w, Failure{Status: http.StatusBadRequest, Type: "idempotency_error"})
			return
		}
		writeJSON(w, map[string]any{"id": prior.id, "object": "customer"})
		return
	}
	s.nextID++
	id := fmt.Sprintf("cus_test_%s_%d", s.prefix, s.nextID)
	s.customers[id] = req.Form.Get("metadata[civic_scanner_user_id]")
	s.byKey[key] = keyed{id: id, params: params}
	writeJSON(w, map[string]any{"id": id, "object": "customer", "email": req.Form.Get("email")})
}

func (s *Server) listSubscriptions(w http.ResponseWriter, req Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	customer := req.Form.Get("customer")
	if _, ok := s.customers[customer]; !ok {
		writeError(w, missingCustomer())
		return
	}
	data := []map[string]any{}
	for i, sub := range s.subscriptions {
		if sub.Customer == customer {
			data = append(data, map[string]any{"id": fmt.Sprintf("sub_test_%d", i+1), "object": "subscription", "customer": sub.Customer, "status": sub.Status})
		}
	}
	writeJSON(w, map[string]any{"object": "list", "url": "/v1/subscriptions", "has_more": false, "data": data})
}

func (s *Server) createSession(w http.ResponseWriter, req Request, prefix, base string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	customer := req.Form.Get("customer")
	if _, ok := s.customers[customer]; !ok {
		writeError(w, missingCustomer())
		return
	}
	s.nextID++
	id := fmt.Sprintf("%s%s_%d", prefix, s.prefix, s.nextID)
	link := base + id
	if s.sessionURL != nil {
		link = *s.sessionURL
	}
	writeJSON(w, map[string]any{"id": id, "customer": customer, "url": link})
}

func missingCustomer() Failure {
	return Failure{Status: http.StatusBadRequest, Type: "invalid_request_error", Code: "resource_missing", Param: "customer"}
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Request-Id", "req_stripetest")
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, f Failure) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Request-Id", "req_stripetest")
	w.WriteHeader(f.Status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"type":    f.Type,
		"code":    f.Code,
		"param":   f.Param,
		"message": "stripetest " + strings.ToLower(http.StatusText(f.Status)) + " " + SecretKey,
	}})
}
