package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
)

func NewRouter(h *Handler, logger *slog.Logger) http.Handler {
	return newRouter(h, logger, adminOperations, func(r chi.Router) {
		v1.HandlerWithOptions(v1.NewStrictHandlerWithOptions(h, []v1.StrictMiddlewareFunc{noStore}, v1.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  invalidBody,
			ResponseErrorHandlerFunc: responseFailed(logger),
		}), v1.ChiServerOptions{
			BaseURL:          "/v1",
			BaseRouter:       r,
			ErrorHandlerFunc: invalidParameter,
		})
	})
}

func newRouter(h *Handler, logger *slog.Logger, admin map[string]adminRoute, routes func(chi.Router)) *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, requestLogger(logger), recoverer(logger), joinHeaderLines("If-None-Match"), authenticate(h.auth, logger), h.adminGate(r, admin))
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Allow", allowedMethods(r, req.URL.Path))
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	})
	routes(r)
	return r
}

func invalidParameter(w http.ResponseWriter, _ *http.Request, err error) {
	writeError(w, http.StatusBadRequest, "invalid_parameter", err.Error())
}

func invalidBody(w http.ResponseWriter, _ *http.Request, err error) {
	writeError(w, http.StatusBadRequest, "invalid_body", err.Error())
}

func responseFailed(logger *slog.Logger) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		logger.ErrorContext(r.Context(), "response failed", "error", err, "request_id", middleware.GetReqID(r.Context()))
		writeError(w, http.StatusInternalServerError, internalError.Code, internalError.Message)
	}
}

func allowedMethods(routes chi.Routes, path string) string {
	var allowed []string
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if routes.Match(chi.NewRouteContext(), method, path) {
			allowed = append(allowed, method)
		}
	}
	return strings.Join(allowed, ", ")
}

func joinHeaderLines(name string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if values := r.Header.Values(name); len(values) > 1 {
				r.Header.Set(name, strings.Join(values, ", "))
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v1.Error{Code: code, Message: message})
}

func requestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			logger.InfoContext(r.Context(), "http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", middleware.GetReqID(r.Context()),
			)
		})
	}
}

func recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if rec == http.ErrAbortHandler { //nolint:errorlint
					panic(rec)
				}
				logger.ErrorContext(r.Context(), "panic recovered",
					"panic", rec,
					"stack", string(debug.Stack()),
					"request_id", middleware.GetReqID(r.Context()),
				)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"code":"internal_error","message":"internal server error"}` + "\n"))
			}()
			next.ServeHTTP(w, r)
		})
	}
}
