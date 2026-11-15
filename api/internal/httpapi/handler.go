package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const healthTimeout = 2 * time.Second

type Store interface {
	Ping(ctx context.Context) error
	ListAgencies(ctx context.Context) ([]store.Agency, error)
}

type Handler struct {
	store   Store
	logger  *slog.Logger
	version string
}

var _ v1.StrictServerInterface = (*Handler)(nil)

func NewHandler(s Store, logger *slog.Logger, version string) *Handler {
	return &Handler{store: s, logger: logger, version: version}
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
		agencies = append(agencies, v1.Agency{Id: a.ID, Slug: a.Slug, Name: a.Name})
	}
	return v1.ListAgencies200JSONResponse{Agencies: agencies}, nil
}
