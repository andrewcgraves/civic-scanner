package billing

import (
	"context"
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
	svc.client = stripe.NewClient(cfg.SecretKey, stripe.WithBackends(backends))
	return svc
}

func (s *Service) Configured() bool { return s != nil && s.client != nil }

func (s *Service) StartCheckout(ctx context.Context, user store.User) (string, error) {
	if !s.Configured() {
		return "", ErrNotConfigured
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	customerID, existed, err := s.ensureCustomer(ctx, user)
	if err != nil {
		return "", err
	}
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
		return "", s.failed(ctx, "create checkout session", err)
	}
	redirect, err := s.redirectURL(ctx, "create checkout session", session.URL)
	if err != nil {
		return "", err
	}
	s.logger.InfoContext(ctx, "checkout session created", "user_id", userID, "checkout_session", session.ID, "customer", customerID)
	return redirect, nil
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
	if err != nil {
		return "", s.failed(ctx, "create billing portal session", err)
	}
	return s.redirectURL(ctx, "create billing portal session", session.URL)
}

func CustomerIdempotencyKey(userID uuid.UUID) string {
	return "civic-scanner-customer-" + userID.String()
}

func (s *Service) ensureCustomer(ctx context.Context, user store.User) (string, bool, error) {
	stored, err := s.store.StripeCustomerID(ctx, user.ID)
	if err != nil {
		return "", false, fmt.Errorf("stripe customer: %w", err)
	}
	if stored != "" {
		return stored, true, nil
	}

	params := &stripe.CustomerCreateParams{Metadata: map[string]string{MetadataUserID: user.ID.String()}}
	if user.Email != nil {
		params.Email = stripe.String(*user.Email)
	}
	params.SetIdempotencyKey(CustomerIdempotencyKey(user.ID))
	customer, err := s.client.V1Customers.Create(ctx, params)
	if err != nil {
		return "", false, s.failed(ctx, "create customer", err)
	}
	if customer.ID == "" {
		return "", false, s.failed(ctx, "create customer", errors.New("response has no customer id"))
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
			return false, s.failed(ctx, "list subscriptions", err)
		}
		if liveStatuses[sub.Status] {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) redirectURL(ctx context.Context, op, raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", s.failed(ctx, op, errors.New("response has no https url"))
	}
	return raw, nil
}

func (s *Service) failed(ctx context.Context, op string, err error) error {
	attrs := []any{"op", op}
	var stripeErr *stripe.Error
	if errors.As(err, &stripeErr) {
		attrs = append(attrs,
			"status", stripeErr.HTTPStatusCode,
			"type", string(stripeErr.Type),
			"code", string(stripeErr.Code),
			"param", stripeErr.Param,
			"request_id", stripeErr.RequestID,
		)
	} else {
		attrs = append(attrs, "error", err.Error())
	}
	s.logger.WarnContext(ctx, "stripe request failed", attrs...)
	return fmt.Errorf("%s: %w", op, ErrUnavailable)
}
