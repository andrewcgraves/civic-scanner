package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/andrewcgraves/civic-scanner/api/internal/auth"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

type adminRoute struct {
	method  string
	pattern string
}

var adminOperations = map[string]adminRoute{}

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
	if identity.Impersonated || identity.Actor != "" {
		return user, errImpersonated
	}
	return user, nil
}

func (h *Handler) adminGate(mux *chi.Mux, operations map[string]adminRoute) func(http.Handler) http.Handler {
	gated := make(map[adminRoute]string, len(operations))
	for operationID, route := range operations {
		gated[route] = operationID
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.RawPath
			if path == "" {
				path = r.URL.Path
			}
			pattern := mux.Find(chi.NewRouteContext(), r.Method, path)
			operationID, ok := gated[adminRoute{method: r.Method, pattern: pattern}]
			if !ok || pattern == "" {
				next.ServeHTTP(w, r)
				return
			}
			ctx := r.Context()
			w.Header().Set("Cache-Control", "no-store")
			admin, err := h.requireAdmin(ctx)
			if err != nil {
				h.refuseAdmin(ctx, w, operationID, admin, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, adminContextKey{}, admin)))
		})
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
