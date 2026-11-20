package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/andrewcgraves/civic-scanner/api/internal/auth"
	"github.com/andrewcgraves/civic-scanner/api/internal/billing"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const FlagBilling = "billing"

type Biller interface {
	Configured() bool
	StartCheckout(ctx context.Context, user store.User) (string, error)
	OpenPortal(ctx context.Context, user store.User) (string, error)
}

func WithBilling(b Biller) HandlerOption { return func(h *Handler) { h.billing = b } }

type billingOutcome int

const (
	billingOK billingOutcome = iota
	billingUnauthorized
	billingNotFound
	billingUnavailable
	billingConflict
	billingFailed
	billingInternal
)

type billingResult struct {
	outcome billingOutcome
	url     string
	body    v1.Error
}

var (
	billingNotConfigured = v1.Error{Code: "billing_not_configured", Message: "billing is not available on this server"}
	billingStripeFailed  = v1.Error{Code: "billing_unavailable", Message: "billing is unavailable right now, try again shortly"}
	alreadySubscribed    = v1.Error{Code: "already_subscribed", Message: "you already have a subscription; manage it in the billing portal"}
	noBillingCustomer    = v1.Error{Code: "no_billing_customer", Message: "there is no billing account yet; start checkout first"}
)

func (h *Handler) billingCall(ctx context.Context, name string, call func(context.Context, store.User) (string, error)) billingResult {
	_, err := IdentityFrom(ctx)
	switch {
	case errors.Is(err, ErrNoCredentials), errors.Is(err, auth.ErrInvalidToken):
		return billingResult{outcome: billingUnauthorized}
	case errors.Is(err, auth.ErrNotConfigured):
		return billingResult{outcome: billingUnavailable, body: authNotConfigured.Body}
	case err != nil:
		return billingResult{outcome: billingUnavailable, body: authUnavailable.Body}
	}
	if !h.enabled(ctx, FlagAccounts) || !h.enabled(ctx, FlagBilling) {
		return billingResult{outcome: billingNotFound}
	}
	if !h.billing.Configured() {
		return billingResult{outcome: billingUnavailable, body: billingNotConfigured}
	}
	user, err := h.requireUser(ctx)
	if err != nil {
		h.logger.ErrorContext(ctx, name+" failed", "error", err)
		return billingResult{outcome: billingInternal}
	}
	redirect, err := call(ctx, user)
	switch {
	case err == nil:
		return billingResult{outcome: billingOK, url: redirect}
	case errors.Is(err, billing.ErrAlreadySubscribed):
		return billingResult{outcome: billingConflict, body: alreadySubscribed}
	case errors.Is(err, billing.ErrNoCustomer):
		return billingResult{outcome: billingConflict, body: noBillingCustomer}
	case errors.Is(err, billing.ErrNotConfigured):
		return billingResult{outcome: billingUnavailable, body: billingNotConfigured}
	case errors.Is(err, billing.ErrUnavailable):
		return billingResult{outcome: billingFailed, body: billingStripeFailed}
	}
	h.logger.ErrorContext(ctx, name+" failed", "error", err)
	return billingResult{outcome: billingInternal}
}

func (h *Handler) CreateCheckoutSession(ctx context.Context, _ v1.CreateCheckoutSessionRequestObject) (v1.CreateCheckoutSessionResponseObject, error) {
	r := h.billingCall(ctx, "create checkout session", h.billing.StartCheckout)
	switch r.outcome {
	case billingOK:
		return v1.CreateCheckoutSession200JSONResponse{BillingRedirectJSONResponse: v1.BillingRedirectJSONResponse{Body: v1.BillingRedirect{Url: r.url}}}, nil
	case billingUnauthorized:
		return v1.CreateCheckoutSession401JSONResponse{UnauthorizedJSONResponse: unauthorized}, nil
	case billingNotFound:
		return v1.CreateCheckoutSession404JSONResponse{NotFoundJSONResponse: v1.NotFoundJSONResponse{Code: "not_found", Message: "not found"}}, nil
	case billingUnavailable:
		return v1.CreateCheckoutSession503JSONResponse{BillingUnavailableJSONResponse: v1.BillingUnavailableJSONResponse{Body: r.body}}, nil
	case billingConflict:
		return v1.CreateCheckoutSession409JSONResponse{BillingConflictJSONResponse: v1.BillingConflictJSONResponse{Body: r.body}}, nil
	case billingFailed:
		return v1.CreateCheckoutSession502JSONResponse{BillingFailedJSONResponse: v1.BillingFailedJSONResponse{Body: r.body}}, nil
	}
	return v1.CreateCheckoutSessiondefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
}

func (h *Handler) CreateBillingPortalSession(ctx context.Context, _ v1.CreateBillingPortalSessionRequestObject) (v1.CreateBillingPortalSessionResponseObject, error) {
	r := h.billingCall(ctx, "create billing portal session", h.billing.OpenPortal)
	switch r.outcome {
	case billingOK:
		return v1.CreateBillingPortalSession200JSONResponse{BillingRedirectJSONResponse: v1.BillingRedirectJSONResponse{Body: v1.BillingRedirect{Url: r.url}}}, nil
	case billingUnauthorized:
		return v1.CreateBillingPortalSession401JSONResponse{UnauthorizedJSONResponse: unauthorized}, nil
	case billingNotFound:
		return v1.CreateBillingPortalSession404JSONResponse{NotFoundJSONResponse: v1.NotFoundJSONResponse{Code: "not_found", Message: "not found"}}, nil
	case billingUnavailable:
		return v1.CreateBillingPortalSession503JSONResponse{BillingUnavailableJSONResponse: v1.BillingUnavailableJSONResponse{Body: r.body}}, nil
	case billingConflict:
		return v1.CreateBillingPortalSession409JSONResponse{BillingConflictJSONResponse: v1.BillingConflictJSONResponse{Body: r.body}}, nil
	case billingFailed:
		return v1.CreateBillingPortalSession502JSONResponse{BillingFailedJSONResponse: v1.BillingFailedJSONResponse{Body: r.body}}, nil
	}
	return v1.CreateBillingPortalSessiondefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
}
