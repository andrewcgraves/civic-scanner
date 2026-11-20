package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/andrewcgraves/civic-scanner/api/internal/auth"
	"github.com/andrewcgraves/civic-scanner/api/internal/featureflags"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const FlagAccounts = "accounts"

type Authenticator interface {
	Configured() bool
	Verify(ctx context.Context, token string) (auth.Identity, error)
}

var ErrNoCredentials = errors.New("no bearer token")

var (
	unauthorized = v1.UnauthorizedJSONResponse{
		Body:    v1.Error{Code: "unauthorized", Message: "a valid bearer token is required"},
		Headers: v1.UnauthorizedResponseHeaders{WWWAuthenticate: "Bearer"},
	}
	authNotConfigured = v1.AuthUnavailableJSONResponse{Body: v1.Error{Code: "auth_not_configured", Message: "sign-in is not available on this server"}}
	authUnavailable   = v1.AuthUnavailableJSONResponse{Body: v1.Error{Code: "auth_unavailable", Message: "sign-in cannot be checked right now, try again shortly"}}
)

var privateOperations = map[string]bool{"GetMe": true}

func noStore(f v1.StrictHandlerFunc, operationID string) v1.StrictHandlerFunc {
	if !privateOperations[operationID] {
		return f
	}
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
		w.Header().Set("Cache-Control", "no-store")
		return f(ctx, w, r, request)
	}
}

type authContextKey struct{}

type authResult struct {
	identity auth.Identity
	err      error
}

func authenticate(a Authenticator, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			header := r.Header.Get("Authorization")
			result := authResult{err: auth.ErrInvalidToken}
			switch token, ok := bearerToken(header); {
			case !a.Configured():
				result.err = auth.ErrNotConfigured
			case header == "":
				result.err = ErrNoCredentials
			case ok:
				result.identity, result.err = a.Verify(ctx, token)
			}
			switch {
			case result.err == nil:
				ctx = featureflags.WithIdentity(ctx, result.identity.UserID)
			case errors.Is(result.err, auth.ErrUnavailable), errors.Is(result.err, auth.ErrInvalidToken):
				logger.DebugContext(ctx, "bearer token not accepted", "error", result.err, "request_id", middleware.GetReqID(ctx))
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, authContextKey{}, result)))
		})
	}
}

func bearerToken(header string) (string, bool) {
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimLeft(token, " ")
	if token == "" || strings.ContainsAny(token, " \t") {
		return "", false
	}
	return token, true
}

func IdentityFrom(ctx context.Context) (auth.Identity, error) {
	result, ok := ctx.Value(authContextKey{}).(authResult)
	if !ok {
		return auth.Identity{}, ErrNoCredentials
	}
	return result.identity, result.err
}

func (h *Handler) requireUser(ctx context.Context) (store.User, error) {
	identity, err := IdentityFrom(ctx)
	if err != nil {
		return store.User{}, err
	}
	user, _, err := h.store.EnsureUser(ctx, store.UserInput{WorkOSUserID: identity.UserID, Email: identity.Email})
	if err != nil {
		return store.User{}, err
	}
	return user, nil
}

func (h *Handler) GetMe(ctx context.Context, _ v1.GetMeRequestObject) (v1.GetMeResponseObject, error) {
	_, err := IdentityFrom(ctx)
	if err == nil && !h.enabled(ctx, FlagAccounts) {
		return v1.GetMe404JSONResponse{NotFoundJSONResponse: v1.NotFoundJSONResponse{Code: "not_found", Message: "not found"}}, nil
	}
	var user store.User
	if err == nil {
		user, err = h.requireUser(ctx)
	}
	switch {
	case errors.Is(err, ErrNoCredentials), errors.Is(err, auth.ErrInvalidToken):
		return v1.GetMe401JSONResponse{UnauthorizedJSONResponse: unauthorized}, nil
	case errors.Is(err, auth.ErrNotConfigured):
		return v1.GetMe503JSONResponse{AuthUnavailableJSONResponse: authNotConfigured}, nil
	case errors.Is(err, auth.ErrUnavailable):
		return v1.GetMe503JSONResponse{AuthUnavailableJSONResponse: authUnavailable}, nil
	case err != nil:
		h.logger.ErrorContext(ctx, "get me failed", "error", err)
		return v1.GetMedefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	}
	return v1.GetMe200JSONResponse{Body: v1.User{Id: user.ID, Email: user.Email, Role: apiRole(user.Role), CreatedAt: user.CreatedAt.UTC()}}, nil
}

func apiRole(r store.Role) v1.UserRole {
	if r.IsAdmin() {
		return v1.UserRoleAdmin
	}
	return v1.UserRoleUser
}
