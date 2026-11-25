package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/andrewcgraves/civic-scanner/api/internal/auth"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

var paidOperations = map[string]string{
	"ListWebhookSubscriptions":        FlagWebhookSubscriptions,
	"CreateWebhookSubscription":       FlagWebhookSubscriptions,
	"GetWebhookSubscription":          FlagWebhookSubscriptions,
	"UpdateWebhookSubscription":       FlagWebhookSubscriptions,
	"DeleteWebhookSubscription":       FlagWebhookSubscriptions,
	"RotateWebhookSubscriptionSecret": FlagWebhookSubscriptions,
	"ListWebhookDeliveries":           FlagWebhookSubscriptions,
	"SendWebhookTest":                 FlagWebhookSubscriptions,
	"PreviewWebhookTemplate":          FlagWebhookSubscriptions,
}

const FlagPlanGranted = "webhook-subscriptions-granted"

var (
	errNotPaid    = errors.New("user is on the free plan")
	errFeatureOff = errors.New("the route's flag is off for this user")
)

var paymentRequired = v1.Error{Code: "payment_required", Message: "this route needs the paid plan"}

type paidContextKey struct{}

type paidUser struct{ store.User }

func (h *Handler) requirePaid(ctx context.Context, flag string) (store.User, error) {
	if _, err := IdentityFrom(ctx); err != nil {
		return store.User{}, err
	}
	if !h.enabled(ctx, FlagAccounts) {
		return store.User{}, errAccountsOff
	}
	if flag != "" && !h.enabled(ctx, flag) {
		return store.User{}, errFeatureOff
	}
	user, err := h.requireUser(ctx)
	if err != nil {
		return store.User{}, err
	}
	if paid, _ := h.planOf(ctx, user); !paid {
		return user, errNotPaid
	}
	return user, nil
}

func (h *Handler) planOf(ctx context.Context, user store.User) (paid, granted bool) {
	if user.Plan.IsPaid() {
		return true, false
	}
	if h.enabled(ctx, FlagPlanGranted) {
		return true, true
	}
	return false, false
}

func (h *Handler) paidGate(operations map[string]string) v1.StrictMiddlewareFunc {
	return func(f v1.StrictHandlerFunc, operationID string) v1.StrictHandlerFunc {
		flag, paid := operations[operationID]
		if !paid {
			return f
		}
		return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
			w.Header().Set("Cache-Control", "no-store")
			user, err := h.requirePaid(ctx, flag)
			if err != nil {
				h.refusePaid(ctx, w, operationID, user, err)
				return nil, nil
			}
			return f(context.WithValue(ctx, paidContextKey{}, paidUser{user}), w, r, request)
		}
	}
}

func (h *Handler) refusePaid(ctx context.Context, w http.ResponseWriter, operationID string, user store.User, err error) {
	switch {
	case writeSignInRefusal(w, err):
	case errors.Is(err, errNotPaid):
		h.logger.InfoContext(ctx, "paid route refused", "operation", operationID, "user_id", user.ID, "plan", string(user.Plan),
			"request_id", middleware.GetReqID(ctx))
		writeError(w, http.StatusPaymentRequired, paymentRequired.Code, paymentRequired.Message)
	default:
		h.logger.ErrorContext(ctx, "paid check failed", "operation", operationID, "error", err, "request_id", middleware.GetReqID(ctx))
		writeError(w, http.StatusInternalServerError, internalError.Code, internalError.Message)
	}
}

func writeSignInRefusal(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, ErrNoCredentials), errors.Is(err, auth.ErrInvalidToken):
		w.Header().Set("WWW-Authenticate", unauthorized.Headers.WWWAuthenticate)
		writeError(w, http.StatusUnauthorized, unauthorized.Body.Code, unauthorized.Body.Message)
	case errors.Is(err, auth.ErrNotConfigured):
		writeError(w, http.StatusServiceUnavailable, authNotConfigured.Body.Code, authNotConfigured.Body.Message)
	case errors.Is(err, auth.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, authUnavailable.Body.Code, authUnavailable.Body.Message)
	case errors.Is(err, errAccountsOff), errors.Is(err, errFeatureOff):
		writeError(w, http.StatusNotFound, "not_found", "not found")
	default:
		return false
	}
	return true
}

func paidFrom(ctx context.Context) (store.User, bool) {
	v, ok := ctx.Value(paidContextKey{}).(paidUser)
	return v.User, ok
}
