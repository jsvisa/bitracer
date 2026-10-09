package btc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Client struct {
	url  string
	user string
	pass string
	hc   *http.Client
}

func New(url, user, pass string) *Client {
	return &Client{url: url, user: user, pass: pass, hc: &http.Client{Timeout: 90 * time.Second}}
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *Client) Call(ctx context.Context, method string, params ...any) (json.RawMessage, error) {
	body, err := json.Marshal(rpcRequest{JSONRPC: "1.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.user, c.pass)
	req.Header.Set("Content-Type", "application/json")
	httpResp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bitcoind http %d", httpResp.StatusCode)
	}
	var rr rpcResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&rr); err != nil {
		return nil, err
	}
	if rr.Error != nil {
		return nil, fmt.Errorf("%s: %s", method, rr.Error.Message)
	}
	return rr.Result, nil
}

type Outpoint struct {
	Txid string `json:"txid"`
	Vout uint32 `json:"vout"`
}

type Info struct {
	Blocks        int64  `json:"blocks"`
	BestBlockHash string `json:"bestblockhash"`
}

func (c *Client) Info(ctx context.Context) (*Info, error) {
	raw, err := c.Call(ctx, "getblockchaininfo")
	if err != nil {
		return nil, err
	}
	var info Info
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func (c *Client) BlockHash(ctx context.Context, height int64) (string, error) {
	raw, err := c.Call(ctx, "getblockhash", height)
	if err != nil {
		return "", err
	}
	var h string
	if err := json.Unmarshal(raw, &h); err != nil {
		return "", err
	}
	return h, nil
}

type Block struct {
	Hash     string `json:"hash"`
	Height   int64  `json:"height"`
	Time     int64  `json:"time"`
	PrevHash string `json:"previousblockhash"`
	Txs      []*Tx  `json:"tx"`
}

type Tx struct {
	Txid        string `json:"txid"`
	BlockHeight int64  `json:"blockheight"`
	Time        int64  `json:"time"`
	BlockTime   int64  `json:"blocktime"`
	Vin         []Vin  `json:"vin"`
	Vout        []Vout `json:"vout"`
}

type Vin struct {
	Coinbase string `json:"coinbase,omitempty"`
	Txid     string `json:"txid,omitempty"`
	Vout     uint32 `json:"vout"`
}

type Vout struct {
	Value        float64 `json:"value"`
	N            uint32  `json:"n"`
	ScriptPubKey struct {
		Address string `json:"address"`
	} `json:"scriptPubKey"`
}

func (c *Client) Block(ctx context.Context, hash string) (*Block, error) {
	raw, err := c.Call(ctx, "getblock", hash, 2)
	if err != nil {
		return nil, err
	}
	var blk Block
	if err := json.Unmarshal(raw, &blk); err != nil {
		return nil, err
	}
	return &blk, nil
}

func (c *Client) RawTx(ctx context.Context, txid string) (*Tx, error) {
	raw, err := c.Call(ctx, "getrawtransaction", txid, 1)
	if err != nil {
		return nil, err
	}
	var tx Tx
	if err := json.Unmarshal(raw, &tx); err != nil {
		return nil, err
	}
	tx.Txid = txid
	return &tx, nil
}

func (c *Client) WaitForNewBlock(ctx context.Context, timeoutSecs int) error {
	_, err := c.Call(ctx, "waitfornewblock", timeoutSecs)
	return err
}

type SpentPrevout struct {
	Txid         string  `json:"txid"`
	Vout         uint32  `json:"vout"`
	SpendingTxid *string `json:"spending_txid"`
	SpendingVin  *int    `json:"spending_vin"`
}

func (c *Client) SpendingPrevout(ctx context.Context, outs []Outpoint) ([]SpentPrevout, error) {
	raw, err := c.Call(ctx, "gettxspendingprevout", outs)
	if err != nil {
		return nil, err
	}
	var res []SpentPrevout
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	return res, nil
}

func Sats(v float64) int64 {
	return int64(v*1e8 + 0.5)
}

func SatsToBTC(s int64) float64 {
	return float64(s) / 1e8
}
