// Package httpx centralizes the outbound-JSON request plumbing shared by the
// notification, label-vendor, LLM and Telegram clients: marshal, send, close,
// and capped response reads.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
)

// DefaultLimit caps response bodies when Request.Limit is unset.
const DefaultLimit = 4 << 20 // 4 MiB

// Request describes one outbound JSON request.
type Request struct {
	Method string // HTTP method; defaults to POST
	URL    string
	Body   any               // JSON-marshaled when non-nil
	Header map[string]string // extra request headers
	Limit  int64             // max response bytes; 0 = DefaultLimit
}

// Do sends r with hc (nil = http.DefaultClient) and returns the response
// status code and body. The body is capped at the request's limit and the
// response is always closed.
func (r Request) Do(ctx context.Context, hc *http.Client) (int, []byte, error) {
	method := r.Method
	if method == "" {
		method = http.MethodPost
	}
	var body io.Reader
	if r.Body != nil {
		encoded, err := json.Marshal(r.Body)
		if err != nil {
			return 0, nil, err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.URL, body)
	if err != nil {
		return 0, nil, err
	}
	if r.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range r.Header {
		req.Header.Set(k, v)
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	limit := r.Limit
	if limit == 0 {
		limit = DefaultLimit
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, raw, nil
}
