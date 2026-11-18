package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/andrewcgraves/civic-scanner/api/internal/featureflags"
	v1 "github.com/andrewcgraves/civic-scanner/api/internal/httpapi/v1"
	"github.com/andrewcgraves/civic-scanner/api/internal/store"
)

const (
	FlagAgencyFeeds   = "agency-rss-feeds"
	feedCacheControl  = "public, max-age=300"
	feedETagHashBytes = 16
)

func WithPublicWebURL(webURL string) HandlerOption {
	return func(h *Handler) { h.webURL = webURL }
}

type feedResult struct {
	feed        renderedFeed
	notModified bool
	notFound    bool
	failed      bool
}

func (h *Handler) agencyFeed(ctx context.Context, slug string, ifNoneMatch *string) feedResult {
	ctx = featureflags.Anonymous(ctx)
	if h.webURL == "" || !h.enabled(ctx, FlagAgencyFeeds) {
		return feedResult{notFound: true}
	}
	feed, err := h.feeds.get(ctx, slug, h.loadFeed)
	if errors.Is(err, store.ErrNotFound) {
		return feedResult{notFound: true}
	}
	if err != nil {
		h.logger.ErrorContext(ctx, "get agency feed failed", "slug", slug, "error", err)
		return feedResult{failed: true}
	}
	return feedResult{feed: feed, notModified: ifNoneMatch != nil && etagMatches(*ifNoneMatch, feed.etag)}
}

func (h *Handler) loadFeed(ctx context.Context, slug string) (renderedFeed, error) {
	feed, err := h.store.GetLiveAgencyFeed(ctx, slug)
	if err != nil {
		return renderedFeed{}, err
	}
	body, err := feedRenderer{webURL: h.webURL, now: time.Now, summaryGrace: agendaSummaryGrace}.render(feed)
	if err != nil {
		return renderedFeed{}, err
	}
	sum := sha256.Sum256(body)
	return renderedFeed{body: body, etag: `"` + hex.EncodeToString(sum[:feedETagHashBytes]) + `"`}, nil
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
	case r.failed:
		return v1.GetAgencyFeeddefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	case r.notModified:
		return v1.GetAgencyFeed304Response{Headers: notModifiedHeaders(r.feed.etag)}, nil
	}
	return v1.GetAgencyFeed200ApplicationrssXmlCharsetUtf8Response{AgencyFeedApplicationrssXmlCharsetUtf8Response: v1.AgencyFeedApplicationrssXmlCharsetUtf8Response{
		Body:          bytes.NewReader(r.feed.body),
		Headers:       feedHeaders(r.feed.etag),
		ContentLength: int64(len(r.feed.body)),
	}}, nil
}

func (h *Handler) HeadAgencyFeed(ctx context.Context, req v1.HeadAgencyFeedRequestObject) (v1.HeadAgencyFeedResponseObject, error) {
	r := h.agencyFeed(ctx, req.Slug, req.Params.IfNoneMatch)
	switch {
	case r.notFound:
		return v1.HeadAgencyFeed404JSONResponse{NotFoundJSONResponse: agencyNotFound}, nil
	case r.failed:
		return v1.HeadAgencyFeeddefaultJSONResponse{StatusCode: http.StatusInternalServerError, Body: internalError}, nil
	case r.notModified:
		return v1.HeadAgencyFeed304Response{Headers: notModifiedHeaders(r.feed.etag)}, nil
	}
	return v1.HeadAgencyFeed200ApplicationrssXmlCharsetUtf8Response{AgencyFeedApplicationrssXmlCharsetUtf8Response: v1.AgencyFeedApplicationrssXmlCharsetUtf8Response{
		Body:          bytes.NewReader(nil),
		Headers:       feedHeaders(r.feed.etag),
		ContentLength: int64(len(r.feed.body)),
	}}, nil
}
