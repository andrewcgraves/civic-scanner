package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/andrewcgraves/civic-scanner/api/internal/auth"
	"github.com/andrewcgraves/civic-scanner/api/internal/billing"
	"github.com/andrewcgraves/civic-scanner/api/internal/featureflags"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
	"github.com/andrewcgraves/civic-scanner/api/internal/webhooks"
)

const healthTimeout = 2 * time.Second

type Store interface {
	Ping(ctx context.Context) error
	ListAgencies(ctx context.Context) ([]store.Agency, error)
	GetLiveAgency(ctx context.Context, slug string) (store.Agency, error)
	ListMeetings(ctx context.Context, agencyID uuid.UUID, params store.ListMeetingsParams) (store.MeetingPage, error)
	GetLiveMeetingDetail(ctx context.Context, id uuid.UUID) (store.MeetingDetail, error)
	GetLiveAgencyFeed(ctx context.Context, slug string) (store.AgencyFeed, error)
	EnsureUser(ctx context.Context, in store.UserInput) (store.User, bool, error)
	CreateAgencySubmission(ctx context.Context, in store.AgencySubmissionInput) (store.AgencySubmission, error)
	ListPendingAgencySubmissions(ctx context.Context) ([]store.PendingAgencySubmission, error)
	ApproveAgencySubmission(ctx context.Context, id uuid.UUID, slug string) (store.ReviewedAgency, error)
	RejectAgencySubmission(ctx context.Context, id uuid.UUID) (store.ReviewedAgency, error)
	ListWebhookSubscriptions(ctx context.Context, userID uuid.UUID) ([]store.WebhookSubscription, error)
	CreateWebhookSubscription(ctx context.Context, in store.WebhookSubscriptionInput) (store.WebhookSubscription, error)
	GetWebhookSubscription(ctx context.Context, userID, id uuid.UUID) (store.WebhookSubscription, error)
	UpdateWebhookSubscription(ctx context.Context, userID, id uuid.UUID, update store.WebhookSubscriptionUpdate) (store.WebhookSubscription, error)
	RotateWebhookSubscriptionSecret(ctx context.Context, userID, id uuid.UUID, ciphertext []byte) (store.WebhookSubscription, error)
	DeleteWebhookSubscription(ctx context.Context, userID, id uuid.UUID) error
}

type Handler struct {
	store   Store
	logger  *slog.Logger
	version string
	auth    Authenticator
	enabled func(ctx context.Context, flag string) bool
	webURL  string
	feeds   *feedCache
	billing Biller

	webhookSealer    *webhooks.Sealer
	webhookAddresses webhooks.AddressCheck

	stripeEvents StripeEventReceiver
}

var _ v1.StrictServerInterface = (*Handler)(nil)

type HandlerOption func(*Handler)

func WithAuthenticator(a Authenticator) HandlerOption { return func(h *Handler) { h.auth = a } }

func WithFlags(enabled func(ctx context.Context, flag string) bool) HandlerOption {
	return func(h *Handler) { h.enabled = enabled }
}

func NewHandler(s Store, logger *slog.Logger, version string, opts ...HandlerOption) *Handler {
	h := &Handler{store: s, logger: logger, version: version, auth: &auth.Verifier{}, enabled: featureflags.Enabled, feeds: newFeedCache(), billing: &billing.Service{}, stripeEvents: &billing.Webhook{}}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

func (h *Handler) GetHealth(ctx context.Context, _ v1.GetHealthRequestObject) (v1.GetHealthResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()

	if err := h.store.Ping(ctx); err != nil {
		h.logger.WarnContext(ctx, "health check failed", "error", err)
		return v1.GetHealth503JSONResponse{Status: v1.Degraded, Version: &h.version}, nil
	}
	return v1.GetHealth200JSONResponse{Status: v1.Ok, Version: &h.version}, nil
}

func (h *Handler) ListAgencies(ctx context.Context, _ v1.ListAgenciesRequestObject) (v1.ListAgenciesResponseObject, error) {
	rows, err := h.store.ListAgencies(ctx)
	if err != nil {
		h.logger.ErrorContext(ctx, "list agencies failed", "error", err)
		return v1.ListAgenciesdefaultJSONResponse{
			StatusCode: http.StatusInternalServerError,
			Body:       v1.Error{Code: "internal_error", Message: "internal server error"},
		}, nil
	}

	agencies := make([]v1.Agency, 0, len(rows))
	for _, a := range rows {
		agencies = append(agencies, apiAgency(a))
	}
	return v1.ListAgencies200JSONResponse{Agencies: agencies}, nil
}
