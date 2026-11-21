package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

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

type routeFinder interface {
	Find(rctx *chi.Context, method, path string) string
}

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

func (h *Handler) adminGate(mux routeFinder, operations map[string]adminRoute) func(http.Handler) http.Handler {
	gated := make(map[adminRoute]string, len(operations))
	for operationID, route := range operations {
		gated[route] = operationID
	}
	return func(next http.Handler) http.Handler {
		if len(gated) == 0 {
			return next
		}
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
	case writeSignInRefusal(w, err):
	case errors.Is(err, errNotAdmin), errors.Is(err, errImpersonated):
		h.logger.InfoContext(ctx, "admin route refused", "operation", operationID, "user_id", user.ID, "reason", err.Error(),
			"request_id", middleware.GetReqID(ctx))
		writeError(w, http.StatusForbidden, forbidden.Code, forbidden.Message)
	default:
		h.logger.ErrorContext(ctx, "admin check failed", "operation", operationID, "error", err, "request_id", middleware.GetReqID(ctx))
		writeError(w, http.StatusInternalServerError, internalError.Code, internalError.Message)
	}
}

func (h *Handler) adminBackstop(operations map[string]adminRoute) v1.StrictMiddlewareFunc {
	return func(f v1.StrictHandlerFunc, operationID string) v1.StrictHandlerFunc {
		if _, ok := operations[operationID]; !ok {
			return f
		}
		return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
			if _, ok := adminFrom(ctx); ok {
				return f(ctx, w, r, request)
			}
			h.logger.WarnContext(ctx, "admin operation reached without passing the admin gate", "operation", operationID,
				"method", r.Method, "path", r.URL.Path, "request_id", middleware.GetReqID(ctx))
			w.Header().Set("Cache-Control", "no-store")
			writeError(w, http.StatusForbidden, forbidden.Code, forbidden.Message)
			return nil, nil
		}
	}
}

func adminFrom(ctx context.Context) (store.User, bool) {
	user, ok := ctx.Value(adminContextKey{}).(store.User)
	return user, ok && user.Role.IsAdmin()
}
