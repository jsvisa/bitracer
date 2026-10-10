package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jsvisa/bitracer/internal/btc"
	"github.com/jsvisa/bitracer/internal/notify"
	"github.com/jsvisa/bitracer/internal/store"
)

func obj(props map[string]any, required ...string) json.RawMessage {
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	b, _ := json.Marshal(schema)
	return b
}

// readTools are available to every allowed chat; writeTools only to admin
// chats (BITRACER_BOT_ADMIN_CHATS).
func toolsFor(admin bool) []ToolDef {
	read := []ToolDef{
		{Type: "function", Function: ToolFunc{Name: "list_cases",
			Description: "List all tracked cases with id, name, status and seed tx counts.",
			Parameters:  obj(map[string]any{})}},
		{Type: "function", Function: ToolFunc{Name: "case_parking",
			Description: "Where a case's funds are parked right now: unspent tracked outputs summed per address, largest first.",
			Parameters: obj(map[string]any{
				"case_id": map[string]any{"type": "integer", "description": "Case id"},
			}, "case_id")}},
		{Type: "function", Function: ToolFunc{Name: "case_trace",
			Description: "Movement history of a case: emitted alerts (hops, terminal hits) newest first, plus its seed transactions. This is the stolen-funds workflow trail.",
			Parameters: obj(map[string]any{
				"case_id": map[string]any{"type": "integer", "description": "Case id"},
				"limit":   map[string]any{"type": "integer", "description": "Max alerts to return (default 20)"},
			}, "case_id")}},
		{Type: "function", Function: ToolFunc{Name: "case_txs",
			Description: "List the seed transactions registered on a case.",
			Parameters: obj(map[string]any{
				"case_id": map[string]any{"type": "integer", "description": "Case id"},
			}, "case_id")}},
		{Type: "function", Function: ToolFunc{Name: "case_channels",
			Description: "List the notification channels a case is subscribed to.",
			Parameters: obj(map[string]any{
				"case_id": map[string]any{"type": "integer", "description": "Case id"},
			}, "case_id")}},
		{Type: "function", Function: ToolFunc{Name: "address_info",
			Description: "Look up one Bitcoin address: known label/entity, terminal status, and whether it is part of any case's watched outputs.",
			Parameters: obj(map[string]any{
				"address": map[string]any{"type": "string", "description": "Bitcoin address"},
			}, "address")}},
		{Type: "function", Function: ToolFunc{Name: "tx_info",
			Description: "Look up one transaction: which block indexed it and its tracked outputs.",
			Parameters: obj(map[string]any{
				"txid": map[string]any{"type": "string", "description": "Transaction id (64 hex chars)"},
			}, "txid")}},
		{Type: "function", Function: ToolFunc{Name: "recent_alerts",
			Description: "Most recent alerts across all cases.",
			Parameters: obj(map[string]any{
				"limit": map[string]any{"type": "integer", "description": "Max alerts (default 10)"},
			})}},
		{Type: "function", Function: ToolFunc{Name: "block_info",
			Description: "Hash and timestamp of an indexed block height.",
			Parameters: obj(map[string]any{
				"height": map[string]any{"type": "integer", "description": "Block height"},
			}, "height")}},
		{Type: "function", Function: ToolFunc{Name: "sync_status",
			Description: "Indexer health: last indexed height, indexing range, default minimum tracked value.",
			Parameters:  obj(map[string]any{})}},
		{Type: "function", Function: ToolFunc{Name: "list_channels",
			Description: "List configured notification channels with their ids.",
			Parameters:  obj(map[string]any{})}},
	}
	if !admin {
		return read
	}
	return append(read,
		ToolDef{Type: "function", Function: ToolFunc{Name: "create_case",
			Description: "Start tracking a new case. Binds at least one notification channel; when channel_ids is omitted the Telegram channel of the asking chat is used.",
			Parameters: obj(map[string]any{
				"name":        map[string]any{"type": "string", "description": "Case name"},
				"min_btc":     map[string]any{"type": "number", "description": "Minimum tracked movement in BTC (optional)"},
				"depth_cap":   map[string]any{"type": "integer", "description": "Max hops to follow (default 50)"},
				"branch_cap":  map[string]any{"type": "integer", "description": "Max branches per hop (default 500)"},
				"channel_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Notification channel ids (optional)"},
			}, "name")}},
		ToolDef{Type: "function", Function: ToolFunc{Name: "set_case_status",
			Description: "Pause or reactivate a case. Paused cases stop tracking; active ones resume.",
			Parameters: obj(map[string]any{
				"case_id": map[string]any{"type": "integer", "description": "Case id"},
				"status":  map[string]any{"type": "string", "enum": []string{"active", "paused"}},
			}, "case_id", "status")}},
		ToolDef{Type: "function", Function: ToolFunc{Name: "add_case_tx",
			Description: "Register a stolen-funds seed transaction on a case; the indexer picks it up on the next pass.",
			Parameters: obj(map[string]any{
				"case_id": map[string]any{"type": "integer", "description": "Case id"},
				"txid":    map[string]any{"type": "string", "description": "Transaction id (64 hex chars)"},
			}, "case_id", "txid")}},
		ToolDef{Type: "function", Function: ToolFunc{Name: "mark_tx_terminal",
			Description: "Stop tracking a transaction's remaining outputs (e.g. funds recovered or reached a known endpoint).",
			Parameters: obj(map[string]any{
				"case_id": map[string]any{"type": "integer", "description": "Case id"},
				"txid":    map[string]any{"type": "string", "description": "Transaction id (64 hex chars)"},
			}, "case_id", "txid")}},
		ToolDef{Type: "function", Function: ToolFunc{Name: "mark_address_terminal",
			Description: "Flag a Bitcoin address as a terminal entity (kind: cex, mixer, gambling, darknet, service, manual) so tracking stops there.",
			Parameters: obj(map[string]any{
				"address": map[string]any{"type": "string", "description": "Bitcoin address"},
				"kind":    map[string]any{"type": "string", "enum": []string{"cex", "mixer", "gambling", "darknet", "service", "manual"}},
			}, "address", "kind")}},
		ToolDef{Type: "function", Function: ToolFunc{Name: "test_channel",
			Description: "Send a test notification to a channel id (see list_channels).",
			Parameters: obj(map[string]any{
				"channel_id": map[string]any{"type": "integer", "description": "Channel id"},
			}, "channel_id")}},
	)
}

type argsCase struct {
	CaseID int64 `json:"case_id"`
}
type argsCaseLimit struct {
	CaseID int64 `json:"case_id"`
	Limit  int   `json:"limit"`
}
type argsAddress struct {
	Address string `json:"address"`
}
type argsTx struct {
	Txid string `json:"txid"`
}
type argsLimit struct {
	Limit int `json:"limit"`
}
type argsHeight struct {
	Height int64 `json:"height"`
}
type argsCreateCase struct {
	Name       string   `json:"name"`
	MinBTC     *float64 `json:"min_btc"`
	DepthCap   *int32   `json:"depth_cap"`
	BranchCap  *int32   `json:"branch_cap"`
	ChannelIDs []int64  `json:"channel_ids"`
}
type argsSetStatus struct {
	CaseID int64  `json:"case_id"`
	Status string `json:"status"`
}
type argsCaseTx struct {
	CaseID int64  `json:"case_id"`
	Txid   string `json:"txid"`
}
type argsAddrTerminal struct {
	Address string `json:"address"`
	Kind    string `json:"kind"`
}
type argsChannel struct {
	ChannelID int64 `json:"channel_id"`
}

// dispatch runs one tool call and returns the result for the model as a
// small JSON object. Write tools re-check the admin flag (defense in depth
// against the model hallucinating a call it was never offered).
func (b *Bot) dispatch(ctx context.Context, name, rawArgs string, chatID int64, admin bool) string {
	var err error
	res := func(v any) string {
		out, _ := json.Marshal(map[string]any{"result": v})
		return string(out)
	}
	fail := func(err error) string {
		out, _ := json.Marshal(map[string]any{"error": err.Error()})
		return string(out)
	}

	st := b.st
	switch name {
	case "list_cases":
		cs, err := st.ListCases(ctx)
		if err != nil {
			return fail(err)
		}
		type caseRow struct {
			ID       int64  `json:"id"`
			Name     string `json:"name"`
			Status   string `json:"status"`
			TxTotal  int64  `json:"seed_txs"`
			TxSeeded int64  `json:"seed_txs_indexed"`
		}
		rows := make([]caseRow, 0, len(cs))
		for _, c := range cs {
			rows = append(rows, caseRow{c.ID, c.Name, c.Status, c.TxTotal, c.TxSeeded})
		}
		return res(rows)

	case "case_parking":
		var a argsCase
		if err = json.Unmarshal([]byte(rawArgs), &a); err != nil {
			return fail(err)
		}
		c, err := st.GetCase(ctx, a.CaseID)
		if err != nil {
			return fail(err)
		}
		hs, err := st.CaseHoldings(ctx, a.CaseID)
		if err != nil {
			return fail(err)
		}
		type hold struct {
			Address string  `json:"address"`
			BTC     float64 `json:"btc"`
		}
		holds := make([]hold, 0, len(hs))
		var total int64
		for _, h := range hs {
			total += h.Sats
			holds = append(holds, hold{h.Address, btc.SatsToBTC(h.Sats)})
		}
		return res(map[string]any{"case": c.Name, "status": c.Status,
			"total_btc": btc.SatsToBTC(total), "parking": holds})

	case "case_trace":
		var a argsCaseLimit
		if err = json.Unmarshal([]byte(rawArgs), &a); err != nil {
			return fail(err)
		}
		if a.Limit <= 0 || a.Limit > 100 {
			a.Limit = 20
		}
		c, err := st.GetCase(ctx, a.CaseID)
		if err != nil {
			return fail(err)
		}
		alerts, err := st.ListAlerts(ctx, a.CaseID, a.Limit)
		if err != nil {
			return fail(err)
		}
		txs, err := st.ListCaseTxs(ctx, a.CaseID)
		if err != nil {
			return fail(err)
		}
		return res(map[string]any{"case": c.Name, "status": c.Status,
			"alerts": alerts, "seed_txids": caseTxIDs(txs)})

	case "case_txs":
		var a argsCase
		if err = json.Unmarshal([]byte(rawArgs), &a); err != nil {
			return fail(err)
		}
		txs, err := st.ListCaseTxs(ctx, a.CaseID)
		if err != nil {
			return fail(err)
		}
		return res(txs)

	case "case_channels":
		var a argsCase
		if err = json.Unmarshal([]byte(rawArgs), &a); err != nil {
			return fail(err)
		}
		chs, err := st.ListCaseChannels(ctx, a.CaseID)
		if err != nil {
			return fail(err)
		}
		type chRow struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
			Type string `json:"type"`
		}
		rows := make([]chRow, 0, len(chs))
		for _, ch := range chs {
			rows = append(rows, chRow{ch.ID, ch.Name, ch.Type})
		}
		return res(rows)

	case "address_info":
		var a argsAddress
		if err = json.Unmarshal([]byte(rawArgs), &a); err != nil {
			return fail(err)
		}
		info, err := st.GetAddress(ctx, a.Address)
		if err != nil {
			return fail(err)
		}
		watching, err := st.WatchingByAddress(ctx, a.Address)
		if err != nil {
			return fail(err)
		}
		type watched struct {
			CaseID int64   `json:"case_id"`
			Txid   string  `json:"txid"`
			BTC    float64 `json:"btc"`
			Status string  `json:"status"`
		}
		rows := make([]watched, 0, len(watching))
		for _, w := range watching {
			rows = append(rows, watched{w.CaseID, w.Txid, btc.SatsToBTC(w.ValueSats), w.Status})
		}
		if info == nil {
			return res(map[string]any{"address": a.Address, "label": "", "watched_outputs": rows})
		}
		return res(map[string]any{"address": info.Address, "label": info.Label, "source": info.Source,
			"is_cex": info.IsCEX, "is_terminal": info.IsTerminal, "terminal_kind": info.TerminalKind,
			"watched_outputs": rows})

	case "tx_info":
		var a argsTx
		if err = json.Unmarshal([]byte(rawArgs), &a); err != nil {
			return fail(err)
		}
		height, err := st.TxHeight(ctx, strings.ToLower(a.Txid))
		if errors.Is(err, pgx.ErrNoRows) {
			return res(map[string]any{"txid": strings.ToLower(a.Txid), "indexed": false})
		}
		if err != nil {
			return fail(err)
		}
		outs, err := st.OutputsForTxids(ctx, []string{strings.ToLower(a.Txid)})
		if err != nil {
			return fail(err)
		}
		type txOut struct {
			Vout    int32   `json:"vout"`
			Address string  `json:"address"`
			BTC     float64 `json:"btc"`
		}
		outs2 := make([]txOut, 0, len(outs))
		for _, o := range outs {
			outs2 = append(outs2, txOut{o.Vout, o.Address, btc.SatsToBTC(o.ValueSats)})
		}
		return res(map[string]any{"txid": strings.ToLower(a.Txid), "indexed": true,
			"height": height, "outputs": outs2})

	case "recent_alerts":
		var a argsLimit
		_ = json.Unmarshal([]byte(rawArgs), &a)
		if a.Limit <= 0 || a.Limit > 100 {
			a.Limit = 10
		}
		alerts, err := st.ListAlerts(ctx, 0, a.Limit)
		if err != nil {
			return fail(err)
		}
		return res(alerts)

	case "block_info":
		var a argsHeight
		if err = json.Unmarshal([]byte(rawArgs), &a); err != nil {
			return fail(err)
		}
		hash, err := st.BlockHash(ctx, a.Height)
		if errors.Is(err, pgx.ErrNoRows) {
			return res(map[string]any{"height": a.Height, "indexed": false})
		}
		if err != nil {
			return fail(err)
		}
		ts, err := st.BlockTime(ctx, a.Height)
		if err != nil {
			return fail(err)
		}
		return res(map[string]any{"height": a.Height, "indexed": true, "hash": hash,
			"time": timeOf(ts)})

	case "sync_status":
		ss, err := st.SyncState(ctx)
		if err != nil {
			return fail(err)
		}
		return res(map[string]any{"last_height": ss.LastHeight,
			"updated_at": ss.UpdatedAt.Format(time.RFC3339)})

	case "list_channels":
		chs, err := st.ListChannels(ctx)
		if err != nil {
			return fail(err)
		}
		type chRow struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
			Type string `json:"type"`
		}
		rows := make([]chRow, 0, len(chs))
		for _, ch := range chs {
			rows = append(rows, chRow{ch.ID, ch.Name, ch.Type})
		}
		return res(rows)

	// ---- write tools (admin chats only) ----

	case "create_case":
		if !admin {
			return fail(errAdminOnly)
		}
		var a argsCreateCase
		if err = json.Unmarshal([]byte(rawArgs), &a); err != nil {
			return fail(err)
		}
		if strings.TrimSpace(a.Name) == "" {
			return fail(fmt.Errorf("name required"))
		}
		ids := a.ChannelIDs
		if len(ids) == 0 {
			ids = b.telegramChannelsForChat(ctx, chatID)
			if len(ids) == 0 {
				return fail(fmt.Errorf("no notification channel bound to this chat; pass channel_ids (see list_channels)"))
			}
		}
		if err = st.ValidateChannelIDs(ctx, ids); err != nil {
			return fail(fmt.Errorf("unknown channel id"))
		}
		var minSats *int64
		if a.MinBTC != nil && *a.MinBTC > 0 {
			v := int64(*a.MinBTC * 1e8)
			minSats = &v
		}
		depthCap, branchCap := int32(50), int32(500)
		if a.DepthCap != nil && *a.DepthCap > 0 {
			depthCap = *a.DepthCap
		}
		if a.BranchCap != nil && *a.BranchCap > 0 {
			branchCap = *a.BranchCap
		}
		c, err := st.CreateCase(ctx, a.Name, minSats, depthCap, branchCap)
		if err != nil {
			return fail(err)
		}
		if err = st.SetCaseChannels(ctx, c.ID, ids); err != nil {
			return fail(err)
		}
		return res(c)

	case "set_case_status":
		if !admin {
			return fail(errAdminOnly)
		}
		var a argsSetStatus
		if err = json.Unmarshal([]byte(rawArgs), &a); err != nil {
			return fail(err)
		}
		if a.Status != "active" && a.Status != "paused" {
			return fail(fmt.Errorf("status must be active or paused"))
		}
		if err = st.SetCaseStatus(ctx, a.CaseID, a.Status); err != nil {
			return fail(err)
		}
		return res(map[string]any{"case_id": a.CaseID, "status": a.Status})

	case "add_case_tx":
		if !admin {
			return fail(errAdminOnly)
		}
		var a argsCaseTx
		if err = json.Unmarshal([]byte(rawArgs), &a); err != nil {
			return fail(err)
		}
		if !isTxid(a.Txid) {
			return fail(fmt.Errorf("txid must be 64 hex chars"))
		}
		if err = st.AddCaseTx(ctx, a.CaseID, strings.ToLower(a.Txid)); err != nil {
			return fail(err)
		}
		return res(map[string]any{"case_id": a.CaseID, "txid": strings.ToLower(a.Txid), "queued_for_indexing": true})

	case "mark_tx_terminal":
		if !admin {
			return fail(errAdminOnly)
		}
		var a argsCaseTx
		if err = json.Unmarshal([]byte(rawArgs), &a); err != nil {
			return fail(err)
		}
		n, err := st.SetTxTerminal(ctx, a.CaseID, strings.ToLower(a.Txid))
		if err != nil {
			return fail(err)
		}
		return res(map[string]any{"case_id": a.CaseID, "outputs_stopped": n})

	case "mark_address_terminal":
		if !admin {
			return fail(errAdminOnly)
		}
		var a argsAddrTerminal
		if err = json.Unmarshal([]byte(rawArgs), &a); err != nil {
			return fail(err)
		}
		if err = st.SetAddressTerminal(ctx, a.Address, a.Kind); err != nil {
			return fail(err)
		}
		return res(map[string]any{"address": a.Address, "terminal_kind": a.Kind})

	case "test_channel":
		if !admin {
			return fail(errAdminOnly)
		}
		var a argsChannel
		if err = json.Unmarshal([]byte(rawArgs), &a); err != nil {
			return fail(err)
		}
		ch, err := b.channelByID(ctx, a.ChannelID)
		if err != nil {
			return fail(err)
		}
		n, err := notify.Build(ch.Type, ch.Config)
		if err != nil {
			return fail(err)
		}
		if err = n.Send(ctx, notify.TestMessage()); err != nil {
			return fail(err)
		}
		return res(map[string]any{"channel_id": a.ChannelID, "sent": true})

	default:
		return fail(fmt.Errorf("unknown tool %q", name))
	}
}

func caseTxIDs(txs []store.CaseTx) []string {
	out := make([]string, 0, len(txs))
	for _, t := range txs {
		out = append(out, t.Txid)
	}
	return out
}

func isTxid(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// channelByID finds one channel by id (there is no direct getter in the store).
func (b *Bot) channelByID(ctx context.Context, id int64) (store.Channel, error) {
	chs, err := b.st.ListChannels(ctx)
	if err != nil {
		return store.Channel{}, err
	}
	for _, ch := range chs {
		if ch.ID == id {
			return ch, nil
		}
	}
	return store.Channel{}, fmt.Errorf("channel %d not found", id)
}

// telegramChannelsForChat returns ids of telegram channels whose chat_id
// matches the asking chat — used as the default channel binding for
// create_case so alerts land back in the chat that started the case.
func (b *Bot) telegramChannelsForChat(ctx context.Context, chatID int64) []int64 {
	chs, err := b.st.ListChannels(ctx)
	if err != nil {
		return nil
	}
	var ids []int64
	for _, ch := range chs {
		if ch.Type != "telegram" {
			continue
		}
		var cfg struct {
			ChatID string `json:"chat_id"`
		}
		if json.Unmarshal(ch.Config, &cfg) != nil {
			continue
		}
		if v, err := strconv.ParseInt(cfg.ChatID, 10, 64); err == nil && v == chatID {
			ids = append(ids, ch.ID)
		}
	}
	return ids
}
