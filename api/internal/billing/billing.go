package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/stripe/stripe-go/v87"

	"github.com/andrewcgraves/civic-scanner/api/internal/config"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const (
	MetadataUserID = "civic_scanner_user_id"

	SuccessPath = "/billing/success"
	CancelPath  = "/billing/cancel"
	ReturnPath  = "/billing"

	operationTimeout  = 8 * time.Second
	attemptTimeout    = 4 * time.Second
	maxNetworkRetries = 1
	subscriptionPage  = 100
)

var (
	ErrNotConfigured     = errors.New("billing is not configured")
	ErrAlreadySubscribed = errors.New("user already has a live subscription")
	ErrNoCustomer        = errors.New("user has no stripe customer")
	ErrUnavailable       = errors.New("stripe request failed")
)

var liveStatuses = map[stripe.SubscriptionStatus]bool{
	stripe.SubscriptionStatusActive:   true,
	stripe.SubscriptionStatusTrialing: true,
	stripe.SubscriptionStatusPastDue:  true,
	stripe.SubscriptionStatusUnpaid:   true,
	stripe.SubscriptionStatusPaused:   true,
}

type CustomerStore interface {
	StripeCustomerID(ctx context.Context, userID uuid.UUID) (string, error)
	SetStripeCustomerID(ctx context.Context, userID uuid.UUID, customerID string) (string, error)
	ClearStripeCustomerID(ctx context.Context, userID uuid.UUID, customerID string) error
}

type Service struct {
	client  *stripe.Client
	store   CustomerStore
	logger  *slog.Logger
	priceID string
	webURL  string
	newKey  func() string
}

type settings struct {
	apiURL     string
	httpClient *http.Client
	noSleep    bool
	logger     *slog.Logger
}

type Option func(*settings)

func WithLogger(l *slog.Logger) Option { return func(s *settings) { s.logger = l } }

func WithAPIURL(u string) Option { return func(s *settings) { s.apiURL = u } }

func WithHTTPClient(c *http.Client) Option { return func(s *settings) { s.httpClient = c } }

func WithoutRetryDelay() Option { return func(s *settings) { s.noSleep = true } }

func New(cfg config.Stripe, webURL string, customers CustomerStore, opts ...Option) *Service {
	s := settings{httpClient: &http.Client{Timeout: attemptTimeout}, logger: slog.New(slog.DiscardHandler)}
	for _, opt := range opts {
		opt(&s)
	}
	svc := &Service{store: customers, logger: s.logger, priceID: cfg.PriceID, webURL: webURL, newKey: uuid.NewString}
	if !cfg.Configured() || webURL == "" || customers == nil {
		return svc
	}
	backendConfig := &stripe.BackendConfig{
		HTTPClient:        s.httpClient,
		LeveledLogger:     &stripe.LeveledLogger{Level: stripe.LevelNull},
		MaxNetworkRetries: stripe.Int64(maxNetworkRetries),
		EnableTelemetry:   stripe.Bool(false),
	}
	if s.apiURL != "" {
		backendConfig.URL = stripe.String(s.apiURL)
	}
	backends := stripe.NewBackendsWithConfig(backendConfig)
	if impl, ok := backends.API.(*stripe.BackendImplementation); ok && s.noSleep {
		impl.SetNetworkRetriesSleep(false)
	}
	svc.client = stripe.NewClient(cfg.SecretKey.Reveal(), stripe.WithBackends(backends))
	return svc
}

func (s *Service) Configured() bool { return s != nil && s.client != nil }

type callError struct {
	op  string
	err error
}

func (e *callError) Error() string { return e.op + ": " + e.err.Error() }

func (e *callError) Unwrap() error { return e.err }

func (s *Service) StartCheckout(ctx context.Context, user store.User) (string, error) {
	if !s.Configured() {
		return "", ErrNotConfigured
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	customerID, existed, err := s.ensureCustomer(ctx, user, "")
	if err != nil {
		return "", s.finish(ctx, err)
	}
	link, err := s.checkout(ctx, user, customerID, existed)
	if existed && isMissingCustomer(err) {
		customerID, err = s.replaceCustomer(ctx, user, customerID, err)
		if err != nil {
			return "", s.finish(ctx, err)
		}
		link, err = s.checkout(ctx, user, customerID, false)
	}
	return link, s.finish(ctx, err)
}

func (s *Service) OpenPortal(ctx context.Context, user store.User) (string, error) {
	if !s.Configured() {
		return "", ErrNotConfigured
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	customerID, err := s.store.StripeCustomerID(ctx, user.ID)
	if err != nil {
		return "", fmt.Errorf("billing portal: %w", err)
	}
	if customerID == "" {
		return "", ErrNoCustomer
	}
	params := &stripe.BillingPortalSessionCreateParams{
		Customer:  stripe.String(customerID),
		ReturnURL: stripe.String(s.webURL + ReturnPath),
	}
	params.SetIdempotencyKey("civic-scanner-portal-" + user.ID.String() + "-" + s.newKey())
	session, err := s.client.V1BillingPortalSessions.Create(ctx, params)
	if isMissingCustomer(err) {
		s.logMissingCustomer(ctx, user, customerID, err)
		if err := s.store.ClearStripeCustomerID(ctx, user.ID, customerID); err != nil {
			return "", fmt.Errorf("billing portal: %w", err)
		}
		return "", ErrNoCustomer
	}
	if err != nil {
		return "", s.finish(ctx, &callError{op: "create billing portal session", err: err})
	}
	link, err := redirectURL("create billing portal session", session.URL)
	return link, s.finish(ctx, err)
}

func CustomerIdempotencyKey(user store.User, replaces string) string {
	email := ""
	if user.Email != nil {
		email = *user.Email
	}
	key := "civic-scanner-customer-" + user.ID.String() + "-" + shortHash(user.ID.String(), email)
	if replaces != "" {
		key += "-replaces-" + shortHash(replaces)
	}
	return key
}

func shortHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

func (s *Service) checkout(ctx context.Context, user store.User, customerID string, existed bool) (string, error) {
	if existed {
		live, err := s.hasLiveSubscription(ctx, customerID)
		if err != nil {
			return "", err
		}
		if live {
			return "", ErrAlreadySubscribed
		}
	}

	userID := user.ID.String()
	params := &stripe.CheckoutSessionCreateParams{
		Mode:              stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		Customer:          stripe.String(customerID),
		ClientReferenceID: stripe.String(userID),
		LineItems: []*stripe.CheckoutSessionCreateLineItemParams{
			{Price: stripe.String(s.priceID), Quantity: stripe.Int64(1)},
		},
		SuccessURL: stripe.String(s.webURL + SuccessPath),
		CancelURL:  stripe.String(s.webURL + CancelPath),
		Metadata:   map[string]string{MetadataUserID: userID},
		SubscriptionData: &stripe.CheckoutSessionCreateSubscriptionDataParams{
			Metadata: map[string]string{MetadataUserID: userID},
		},
	}
	params.SetIdempotencyKey("civic-scanner-checkout-" + userID + "-" + s.newKey())
	session, err := s.client.V1CheckoutSessions.Create(ctx, params)
	if err != nil {
		return "", &callError{op: "create checkout session", err: err}
	}
	link, err := redirectURL("create checkout session", session.URL)
	if err != nil {
		return "", err
	}
	s.logger.InfoContext(ctx, "checkout session created", "user_id", userID, "checkout_session", session.ID, "customer", customerID)
	return link, nil
}

func (s *Service) replaceCustomer(ctx context.Context, user store.User, dead string, cause error) (string, error) {
	s.logMissingCustomer(ctx, user, dead, cause)
	if err := s.store.ClearStripeCustomerID(ctx, user.ID, dead); err != nil {
		return "", fmt.Errorf("stripe customer: %w", err)
	}
	customerID, _, err := s.ensureCustomer(ctx, user, dead)
	return customerID, err
}

func (s *Service) ensureCustomer(ctx context.Context, user store.User, replaces string) (string, bool, error) {
	stored, err := s.store.StripeCustomerID(ctx, user.ID)
	if err != nil {
		return "", false, fmt.Errorf("stripe customer: %w", err)
	}
	if stored != "" && stored != replaces {
		return stored, replaces == "", nil
	}

	params := &stripe.CustomerCreateParams{Metadata: map[string]string{MetadataUserID: user.ID.String()}}
	if user.Email != nil {
		params.Email = stripe.String(*user.Email)
	}
	params.SetIdempotencyKey(CustomerIdempotencyKey(user, replaces))
	customer, err := s.client.V1Customers.Create(ctx, params)
	if err != nil {
		return "", false, &callError{op: "create customer", err: err}
	}
	if customer.ID == "" {
		return "", false, &callError{op: "create customer", err: errors.New("response has no customer id")}
	}
	stored, err = s.store.SetStripeCustomerID(ctx, user.ID, customer.ID)
	if err != nil {
		return "", false, fmt.Errorf("stripe customer: %w", err)
	}
	if stored != customer.ID {
		s.logger.WarnContext(ctx, "another request stored a different stripe customer first", "user_id", user.ID.String(), "kept", stored, "unused", customer.ID)
	}
	return stored, false, nil
}

func (s *Service) hasLiveSubscription(ctx context.Context, customerID string) (bool, error) {
	params := &stripe.SubscriptionListParams{Customer: stripe.String(customerID), Status: stripe.String("all")}
	params.Limit = stripe.Int64(subscriptionPage)
	for sub, err := range s.client.V1Subscriptions.List(ctx, params).All(ctx) {
		if err != nil {
			return false, &callError{op: "list subscriptions", err: err}
		}
		if liveStatuses[sub.Status] {
			return true, nil
		}
	}
	return false, nil
}

func redirectURL(op, raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", &callError{op: op, err: errors.New("response has no https url")}
	}
	return raw, nil
}

func isMissingCustomer(err error) bool {
	var stripeErr *stripe.Error
	return errors.As(err, &stripeErr) && stripeErr.Code == stripe.ErrorCodeResourceMissing && stripeErr.Param == "customer"
}

func (s *Service) logMissingCustomer(ctx context.Context, user store.User, customerID string, err error) {
	attrs := []any{"user_id", user.ID.String(), "customer", customerID}
	var stripeErr *stripe.Error
	if errors.As(err, &stripeErr) {
		attrs = append(attrs, "request_id", stripeErr.RequestID)
	}
	s.logger.WarnContext(ctx, "the stored stripe customer no longer exists; clearing it", attrs...)
}

func (s *Service) finish(ctx context.Context, err error) error {
	var call *callError
	if !errors.As(err, &call) {
		return err
	}
	attrs := []any{"op", call.op}
	var stripeErr *stripe.Error
	if errors.As(call.err, &stripeErr) {
		attrs = append(attrs,
			"status", stripeErr.HTTPStatusCode,
			"type", string(stripeErr.Type),
			"code", string(stripeErr.Code),
			"param", stripeErr.Param,
			"request_id", stripeErr.RequestID,
		)
	} else {
		attrs = append(attrs, "error", call.err.Error())
	}
	s.logger.WarnContext(ctx, "stripe request failed", attrs...)
	return fmt.Errorf("%s: %w", call.op, ErrUnavailable)
}
