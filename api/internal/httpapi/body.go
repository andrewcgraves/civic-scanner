package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
)

type bodyContextKey struct{}

type capturedBody struct {
	contentType string
	data        []byte
	err         error
}

type failingReader struct{ err error }

func (f failingReader) Read([]byte) (int, error) { return 0, f.err }

func captureBody(limit int64, pathLimits map[string]int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bodyLimit := limit
			if pathLimit, ok := pathLimits[r.URL.Path]; ok {
				bodyLimit = pathLimit
			}
			data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, bodyLimit))
			body := capturedBody{contentType: r.Header.Get("Content-Type"), data: data, err: err}
			if err != nil {
				r.Body = io.NopCloser(failingReader{err: err})
			} else {
				r.Body = io.NopCloser(bytes.NewReader(data))
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), bodyContextKey{}, body)))
		})
	}
}

var (
	errBodyTooLarge     = errors.New("the request body is too large")
	errNotJSON          = errors.New("the request body must be application/json")
	errNotObject        = errors.New("the body must be a JSON object with string name and website")
	errDuplicateField   = errors.New("the body names a field more than once")
	errUnknownField     = errors.New("the body may only have the fields name and website")
	errFieldNotString   = errors.New("name and website must be strings")
	errTrailingData     = errors.New("the body has data after the JSON object")
	submissionBodyField = map[string]bool{"name": true, "website": true}
)

func isJSONContentType(value string) bool {
	mediaType, params, err := mime.ParseMediaType(value)
	if err != nil || mediaType != "application/json" {
		return false
	}
	charset, ok := params["charset"]
	return !ok || strings.EqualFold(charset, "utf-8")
}

func readSubmissionBody(ctx context.Context) (map[string]string, error) {
	body, _ := ctx.Value(bodyContextKey{}).(capturedBody)
	if _, tooLarge := errors.AsType[*http.MaxBytesError](body.err); tooLarge {
		return nil, errBodyTooLarge
	}
	if body.err != nil {
		return nil, errNotObject
	}
	if !isJSONContentType(body.contentType) {
		return nil, errNotJSON
	}
	return parseSubmissionBody(body.data)
}

func parseSubmissionBody(data []byte) (map[string]string, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errNotObject
	}
	fields := map[string]string{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, errNotObject
		}
		key, _ := tok.(string)
		if _, seen := fields[key]; seen {
			return nil, errDuplicateField
		}
		if !submissionBodyField[key] {
			return nil, errUnknownField
		}
		value, err := dec.Token()
		if err != nil {
			return nil, errNotObject
		}
		text, ok := value.(string)
		if !ok {
			return nil, errFieldNotString
		}
		fields[key] = text
	}
	if _, err := dec.Token(); err != nil {
		return nil, errNotObject
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errTrailingData
	}
	return fields, nil
}
