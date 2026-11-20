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

var adminOperations = map[string]bool{}

var (
	errAccountsOff  = errors.New("accounts are off for this user")
	errNotAdmin     = errors.New("user is not an admin")
	errImpersonated = errors.New("impersonated session")
)

var forbidden = v1.Error{Code: "forbidden", Message: "this route is for admins only"}

type adminContextKey struct{}

func (h *Handler) requireAdmin(ctx context.Context) (store.User, error) {
	identity, err := IdentityFrom(ctx)
	if err != nil {
		return store.User{}, err
	}
	if !h.enabled(ctx, FlagAccounts) {
		return store.User{}, errAccountsOff
	}
	user, err := h.requireUser(ctx)
	if err != nil {
		return store.User{}, err
	}
	if !user.Role.IsAdmin() {
		return user, errNotAdmin
	}
	if identity.Actor != "" {
		return user, errImpersonated
	}
	return user, nil
}

func (h *Handler) adminOnly(operations map[string]bool) v1.StrictMiddlewareFunc {
	return func(f v1.StrictHandlerFunc, operationID string) v1.StrictHandlerFunc {
		if !operations[operationID] {
			return f
		}
		return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
			w.Header().Set("Cache-Control", "no-store")
			admin, err := h.requireAdmin(ctx)
			if err != nil {
				h.refuseAdmin(ctx, w, operationID, admin, err)
				return nil, nil
			}
			return f(context.WithValue(ctx, adminContextKey{}, admin), w, r, request)
		}
	}
}

func (h *Handler) refuseAdmin(ctx context.Context, w http.ResponseWriter, operationID string, user store.User, err error) {
	switch {
	case errors.Is(err, ErrNoCredentials), errors.Is(err, auth.ErrInvalidToken):
		w.Header().Set("WWW-Authenticate", unauthorized.Headers.WWWAuthenticate)
		writeError(w, http.StatusUnauthorized, unauthorized.Body.Code, unauthorized.Body.Message)
	case errors.Is(err, auth.ErrNotConfigured):
		writeError(w, http.StatusServiceUnavailable, authNotConfigured.Body.Code, authNotConfigured.Body.Message)
	case errors.Is(err, auth.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, authUnavailable.Body.Code, authUnavailable.Body.Message)
	case errors.Is(err, errAccountsOff):
		writeError(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, errNotAdmin), errors.Is(err, errImpersonated):
		h.logger.InfoContext(ctx, "admin route refused", "operation", operationID, "user_id", user.ID, "reason", err.Error(),
			"request_id", middleware.GetReqID(ctx))
		writeError(w, http.StatusForbidden, forbidden.Code, forbidden.Message)
	default:
		h.logger.ErrorContext(ctx, "admin check failed", "operation", operationID, "error", err, "request_id", middleware.GetReqID(ctx))
		writeError(w, http.StatusInternalServerError, internalError.Code, internalError.Message)
	}
}

func adminFrom(ctx context.Context) (store.User, bool) {
	user, ok := ctx.Value(adminContextKey{}).(store.User)
	return user, ok && user.Role.IsAdmin()
}
