package labels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	DefaultAPIURL  = "https://aml.blocksec.com/address-label/api/v3/labels"
	BitcoinChainID = -1
)

type Blocksec struct {
	url     string
	apiKey  string
	chainID int
	hc      *http.Client
}

func NewBlocksec(apiURL, apiKey string, chainID int) *Blocksec {
	if apiURL == "" {
		apiURL = DefaultAPIURL
	}
	return &Blocksec{url: strings.TrimRight(apiURL, "/"), apiKey: apiKey, chainID: chainID, hc: &http.Client{Timeout: 15 * time.Second}}
}

func (b *Blocksec) Name() string { return "blocksec" }

type blocksecCategory struct {
	Name string `json:"name"`
	Code int    `json:"code"`
}

type blocksecEntityInfo struct {
	Entity     string             `json:"entity"`
	Categories []blocksecCategory `json:"categories"`
}

type blocksecData struct {
	ChainID        int                 `json:"chain_id"`
	Address        string              `json:"address"`
	MainEntity     string              `json:"main_entity"`
	MainEntityInfo *blocksecEntityInfo `json:"main_entity_info"`
	NameTag        string              `json:"name_tag"`
}

type blocksecResponse struct {
	Code    int           `json:"code"`
	Message string        `json:"message"`
	Data    *blocksecData `json:"data"`
}

func (b *Blocksec) Lookup(ctx context.Context, address string) (*Label, error) {
	if b.apiKey == "" {
		return nil, nil
	}
	body, err := json.Marshal(map[string]any{"chain_id": b.chainID, "address": address})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("API-KEY", b.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("blocksec http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var r blocksecResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if r.Code != 200000 {
		return nil, fmt.Errorf("blocksec %d: %s", r.Code, r.Message)
	}
	if r.Data == nil {
		return nil, nil
	}
	name := r.Data.MainEntity
	if name == "" {
		name = r.Data.NameTag
	}
	if r.Data.MainEntityInfo != nil && name == "" {
		name = r.Data.MainEntityInfo.Entity
	}
	isCEX := false
	if r.Data.MainEntityInfo != nil {
		for _, c := range r.Data.MainEntityInfo.Categories {
			if strings.EqualFold(c.Name, "EXCHANGE") {
				isCEX = true
				break
			}
		}
	}
	if name == "" && !isCEX {
		return nil, nil
	}
	return &Label{Name: name, Source: b.Name(), IsCEX: isCEX}, nil
}
