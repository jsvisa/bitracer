package labels

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Blocksec struct {
	url string
	key string
	hc  *http.Client
}

func NewBlocksec(apiURL, apiKey string) *Blocksec {
	return &Blocksec{url: strings.TrimRight(apiURL, "/"), key: apiKey, hc: &http.Client{Timeout: 15 * time.Second}}
}

func (b *Blocksec) Lookup(ctx context.Context, address string) (*Label, error) {
	if b.url == "" {
		return nil, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.url+"?chain=bitcoin&address="+url.QueryEscape(address), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", b.key)
	resp, err := b.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("blocksec http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return parseBlocksec(body, address), nil
}

type blocksecLabel struct {
	Name     string `json:"name"`
	Category string `json:"category"`
}

type blocksecResponse struct {
	Name    string          `json:"name"`
	Label   string          `json:"label"`
	Entity  string          `json:"entity"`
	IsCEX   bool            `json:"is_cex"`
	Labels  []blocksecLabel `json:"labels"`
	RawJSON json.RawMessage `json:"-"`
}

func parseBlocksec(body []byte, address string) *Label {
	var r blocksecResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil
	}
	name := firstNonEmpty(r.Name, r.Label, r.Entity)
	isCEX := r.IsCEX
	for _, l := range r.Labels {
		if name == "" {
			name = l.Name
		}
		if isCEXCategory(l.Category, l.Name) {
			isCEX = true
		}
		if name != "" && isCEX {
			break
		}
	}
	if isCEXCategory(name, name) {
		isCEX = true
	}
	if name == "" && !isCEX {
		return nil
	}
	return &Label{Name: name, Source: "blocksec", IsCEX: isCEX}
}

func isCEXCategory(parts ...string) bool {
	for _, p := range parts {
		lp := strings.ToLower(p)
		if strings.Contains(lp, "exchange") || strings.Contains(lp, "cex") {
			return true
		}
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
