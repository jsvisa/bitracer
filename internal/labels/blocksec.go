package labels

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jsvisa/bitracer/internal/httpx"
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

// TerminalCategories maps vendor category names to terminal kinds; a match
// means the walker stops when funds reach the address.
var TerminalCategories = map[string]string{
	"EXCHANGE": "cex",
	"MIXER":    "mixer",
	"GAMBLING": "gambling",
	"DARKNET":  "darknet",
	"SERVICE":  "service",
}

func (b *Blocksec) Lookup(ctx context.Context, address string) (*Label, error) {
	if b.apiKey == "" {
		return nil, nil
	}
	status, raw, err := httpx.Request{
		URL:    b.url,
		Body:   map[string]any{"chain_id": b.chainID, "address": address},
		Header: map[string]string{"API-KEY": b.apiKey},
		Limit:  1 << 20,
	}.Do(ctx, b.hc)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("blocksec http %d: %s", status, strings.TrimSpace(string(raw)))
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
	kind := ""
	if r.Data.MainEntityInfo != nil {
		for _, c := range r.Data.MainEntityInfo.Categories {
			if k, ok := TerminalCategories[strings.ToUpper(strings.TrimSpace(c.Name))]; ok {
				kind = k
				isCEX = k == "cex"
				break
			}
		}
	}
	if name == "" && kind != "" {
		name = kind
	}
	if name == "" && !isCEX {
		return nil, nil
	}
	return &Label{Name: name, Source: b.Name(), IsCEX: isCEX, Kind: kind}, nil
}
