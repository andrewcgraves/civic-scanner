package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/andrewcgraves/civic-scanner/api/internal/featureflags"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const (
	FeedsFlag         = "agency-rss-feeds"
	feedCacheControl  = "public, max-age=300"
	feedETagHashBytes = 16
)

type FeedConfig struct {
	WebURL  string
	Enabled func(ctx context.Context, flag string) bool
}

func (h *Handler) WithFeeds(cfg FeedConfig) *Handler {
	if cfg.Enabled == nil {
		cfg.Enabled = featureflags.Enabled
	}
	h.feeds = cfg
	return h
}

type feedResult struct {
	body        []byte
	etag        string
	notModified bool
	notFound    bool
	err         bool
}

func (h *Handler) agencyFeed(ctx context.Context, slug string, ifNoneMatch *string) feedResult {
	if h.feeds.WebURL == "" || h.feeds.Enabled == nil || !h.feeds.Enabled(ctx, FeedsFlag) {
		return feedResult{notFound: true}
	}
	feed, err := h.store.GetLiveAgencyFeed(ctx, slug)
	if errors.Is(err, store.ErrNotFound) {
		return feedResult{notFound: true}
	}
	if err != nil {
		h.logger.ErrorContext(ctx, "get agency feed failed", "slug", slug, "error", err)
		return feedResult{err: true}
	}
	body, err := renderFeed(feed, h.feeds.WebURL)
	if err != nil {
		h.logger.ErrorContext(ctx, "render agency feed failed", "slug", slug, "error", err)
		return feedResult{err: true}
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:feedETagHashBytes]) + `"`
	return feedResult{body: body, etag: etag, notModified: ifNoneMatch != nil && etagMatches(*ifNoneMatch, etag)}
}

func etagMatches(header, etag string) bool {
	for candidate := range strings.SplitSeq(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}

func feedHeaders(etag string) v1.AgencyFeedResponseHeaders {
	cacheControl := feedCacheControl
	return v1.AgencyFeedResponseHeaders{ETag: &etag, CacheControl: &cacheControl}
}

func notModifiedHeaders(etag string) v1.NotModifiedResponseHeaders {
	cacheControl := feedCacheControl
	return v1.NotModifiedResponseHeaders{ETag: &etag, CacheControl: &cacheControl}
}

func (h *Handler) GetAgencyFeed(ctx context.Context, req v1.GetAgencyFeedRequestObject) (v1.GetAgencyFeedResponseObject, error) {
	r := h.agencyFeed(ctx, req.Slug, req.Params.IfNoneMatch)
	switch {
	case r.notFound:
		return v1.GetAgencyFeed404JSONResponse{NotFoundJSONResponse: agencyNotFound}, nil
	case r.err:
		return v1.GetAgencyFeeddefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	case r.notModified:
		return v1.GetAgencyFeed304Response{Headers: notModifiedHeaders(r.etag)}, nil
	}
	return v1.GetAgencyFeed200ApplicationrssXmlCharsetUtf8Response{AgencyFeedApplicationrssXmlCharsetUtf8Response: v1.AgencyFeedApplicationrssXmlCharsetUtf8Response{
		Body:          bytes.NewReader(r.body),
		Headers:       feedHeaders(r.etag),
		ContentLength: int64(len(r.body)),
	}}, nil
}

func (h *Handler) HeadAgencyFeed(ctx context.Context, req v1.HeadAgencyFeedRequestObject) (v1.HeadAgencyFeedResponseObject, error) {
	r := h.agencyFeed(ctx, req.Slug, req.Params.IfNoneMatch)
	switch {
	case r.notFound:
		return v1.HeadAgencyFeed404JSONResponse{NotFoundJSONResponse: agencyNotFound}, nil
	case r.err:
		return v1.HeadAgencyFeeddefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	case r.notModified:
		return v1.HeadAgencyFeed304Response{Headers: notModifiedHeaders(r.etag)}, nil
	}
	return v1.HeadAgencyFeed200ApplicationrssXmlCharsetUtf8Response{AgencyFeedApplicationrssXmlCharsetUtf8Response: v1.AgencyFeedApplicationrssXmlCharsetUtf8Response{
		Body:          bytes.NewReader(nil),
		Headers:       feedHeaders(r.etag),
		ContentLength: int64(len(r.body)),
	}}, nil
}
