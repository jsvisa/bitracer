package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jsvisa/bitracer/internal/btc"
)

const ExplorerTxBase = "https://mempool.space/tx/"

func ExplorerTxURL(txid string) string { return ExplorerTxBase + txid }

type Message struct {
	Kind      string
	CaseID    int64
	Headline  string
	Txid      string
	Address   string
	Entity    string
	ValueSats int64
	Depth     int32
	Height    string
}

func TestMessage() Message {
	return Message{Kind: "test", Headline: "test notification — bitracer can reach this channel"}
}

// Plain renders the flat one-liner stored in the alerts table.
func (m Message) Plain() string {
	return fmt.Sprintf("[bitracer] case#%d: %s", m.CaseID, m.Headline)
}

// fields returns the labeled detail rows shared by the pretty renderers.
func (m Message) fields() [][2]string {
	var f [][2]string
	add := func(k, v string) {
		if v != "" {
			f = append(f, [2]string{k, v})
		}
	}
	add("case", fmt.Sprintf("#%d", m.CaseID))
	if m.ValueSats > 0 {
		add("value", fmt.Sprintf("%.8f BTC", btc.SatsToBTC(m.ValueSats)))
	}
	if m.Entity != "" {
		add("entity", m.Entity)
	}
	if m.Address != "" {
		add("address", m.Address)
	}
	if m.Depth > 0 {
		add("depth", fmt.Sprintf("%d", m.Depth))
	}
	if m.Height != "" {
		add("status", m.Height)
	}
	return f
}

func shortTx(txid string) string {
	if len(txid) <= 12 {
		return txid
	}
	return txid[:10] + "…"
}

func shortAddr(addr string) string {
	if len(addr) <= 20 {
		return addr
	}
	return addr[:8] + "…" + addr[len(addr)-6:]
}

type Notifier interface {
	Name() string
	Send(ctx context.Context, msg Message) error
}

func Build(typ string, raw json.RawMessage) (Notifier, error) {
	switch typ {
	case "slack":
		var cfg struct {
			Webhook string `json:"webhook"`
			Channel string `json:"channel"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("slack config: %w", err)
		}
		if cfg.Webhook == "" {
			return nil, fmt.Errorf("slack config requires webhook")
		}
		return NewSlack(cfg.Webhook, cfg.Channel), nil
	case "telegram":
		var cfg struct {
			Token  string `json:"token"`
			ChatID string `json:"chat_id"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("telegram config: %w", err)
		}
		if cfg.Token == "" || cfg.ChatID == "" {
			return nil, fmt.Errorf("telegram config requires token and chat_id")
		}
		return NewTelegram(cfg.Token, cfg.ChatID), nil
	case "lark":
		var cfg struct {
			Webhook string `json:"webhook"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("lark config: %w", err)
		}
		if cfg.Webhook == "" {
			return nil, fmt.Errorf("lark config requires webhook")
		}
		return NewLark(cfg.Webhook), nil
	default:
		return nil, fmt.Errorf("unknown channel type %q", typ)
	}
}

func SendAll(ctx context.Context, notifiers []Notifier, msg Message) {
	var wg sync.WaitGroup
	for _, n := range notifiers {
		wg.Add(1)
		go func(n Notifier) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := n.Send(ctx, msg); err != nil {
				slog.Error("notify failed", "channel", n.Name(), "err", err)
			}
		}(n)
	}
	wg.Wait()
}

func postJSON(ctx context.Context, url string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return nil
}

func kindLabel(m Message) string {
	switch m.Kind {
	case "":
		return "alert"
	case "test":
		return "test"
	default:
		return m.Kind
	}
}

// IsTerminalKind reports whether the kind means tracking stopped at a known
// entity (exchange, mixer, service, ...) — rendered green, unlike plain
// movement alerts.
func IsTerminalKind(kind string) bool {
	switch kind {
	case "cex", "mixer", "gambling", "darknet", "service", "manual", "test":
		return true
	}
	return false
}

// Slack renders Block Kit: colored-attachment style with mrkdwn fields
// and a mempool.space link on the txhash. channel overrides the webhook's
// bound channel (supported by legacy incoming webhooks).
type Slack struct {
	webhook string
	channel string
}

func NewSlack(webhook, channel string) *Slack { return &Slack{webhook: webhook, channel: channel} }

func (s *Slack) Name() string { return "slack" }

func (s *Slack) Send(ctx context.Context, msg Message) error {
	var b strings.Builder
	for _, f := range msg.fields() {
		v := f[1]
		if f[0] == "address" {
			v = "`" + shortAddr(v) + "`"
		}
		fmt.Fprintf(&b, "*%s:* %s\n", f[0], v)
	}
	headline := msg.Headline
	if headline == "" {
		headline = msg.Plain()
	}
	// Collapse a full 64-char txid in the headline to a linked short form,
	// and skip the separate tx field when the headline already shows it —
	// otherwise the txid renders three times in one message.
	if msg.Txid != "" {
		link := "<" + ExplorerTxURL(msg.Txid) + "|" + shortTx(msg.Txid) + ">"
		headline = strings.ReplaceAll(headline, msg.Txid, link)
		if !strings.Contains(headline, shortTx(msg.Txid)) {
			fmt.Fprintf(&b, "*tx:* %s\n", link)
		}
	}
	color := "#bf616a"
	if IsTerminalKind(msg.Kind) {
		color = "#a3be8c"
	}
	// One short line as the notification text — Slack renders payload-level
	// text in-app above the card, and rejects attachments[].text outright
	// (invalid_attachments) when blocks are present, so keep it payload-level.
	fallback := fmt.Sprintf("[bitracer] case#%d · %s", msg.CaseID, kindLabel(msg))
	if msg.Headline != "" {
		fallback += " — " + strings.ReplaceAll(msg.Headline, msg.Txid, shortTx(msg.Txid))
	}
	blocks := []any{
		map[string]any{
			"type": "header",
			"text": map[string]string{"type": "plain_text", "text": truncate("bitracer · "+kindLabel(msg), 140)},
		},
		map[string]any{
			"type": "section",
			"text": map[string]string{"type": "mrkdwn", "text": strings.TrimSpace(headline)},
		},
	}
	if fields := strings.TrimRight(b.String(), "\n"); fields != "" {
		blocks = append(blocks, map[string]any{
			"type": "section",
			"text": map[string]string{"type": "mrkdwn", "text": fields},
		})
	}
	payload := map[string]any{
		"text": fallback,
		"attachments": []any{
			map[string]any{
				"color":  color,
				"blocks": blocks,
			},
		},
	}
	if s.channel != "" {
		payload["channel"] = s.channel
	}
	return postJSON(ctx, s.webhook, payload)
}

// Telegram renders HTML with bold labels and a mempool.space link.
type Telegram struct {
	token string
	chat  string
}

func NewTelegram(token, chat string) *Telegram { return &Telegram{token: token, chat: chat} }

func (t *Telegram) Name() string { return "telegram" }

func (t *Telegram) Send(ctx context.Context, msg Message) error {
	var b strings.Builder
	fmt.Fprintf(&b, "<b>bitracer · %s</b>\n", kindLabel(msg))
	fmt.Fprintf(&b, "<b>%s</b>\n", escapeHTML(msg.Headline))
	for _, f := range msg.fields() {
		fmt.Fprintf(&b, "<b>%s:</b> %s\n", escapeHTML(f[0]), escapeHTML(f[1]))
	}
	if msg.Txid != "" {
		fmt.Fprintf(&b, "<b>tx:</b> <a href=\"%s\">%s</a>\n", ExplorerTxURL(msg.Txid), shortTx(msg.Txid))
	}
	return postJSON(ctx, "https://api.telegram.org/bot"+t.token+"/sendMessage",
		map[string]string{"chat_id": t.chat, "text": strings.TrimSpace(b.String()), "parse_mode": "HTML"})
}

// Lark renders an interactive card with markdown elements and a
// mempool.space link.
type Lark struct{ webhook string }

func NewLark(webhook string) *Lark { return &Lark{webhook: webhook} }

func (l *Lark) Name() string { return "lark" }

func (l *Lark) Send(ctx context.Context, msg Message) error {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s**\n", msg.Headline)
	for _, f := range msg.fields() {
		fmt.Fprintf(&b, "- **%s:** %s\n", f[0], f[1])
	}
	if msg.Txid != "" {
		fmt.Fprintf(&b, "- **tx:** [%s](%s)\n", shortTx(msg.Txid), ExplorerTxURL(msg.Txid))
	}
	template := "red"
	if IsTerminalKind(msg.Kind) {
		template = "green"
	}
	payload := map[string]any{
		"msg_type": "interactive",
		"card": map[string]any{
			"header": map[string]any{
				"title":    map[string]string{"tag": "plain_text", "content": truncate("bitracer · "+kindLabel(msg), 100)},
				"template": template,
			},
			"elements": []any{
				map[string]string{"tag": "markdown", "content": strings.TrimSpace(b.String())},
				map[string]any{"tag": "hr"},
				map[string]string{"tag": "plain_text", "content": "sent by bitracer"},
			},
		},
	}
	return postJSON(ctx, l.webhook, payload)
}

func escapeHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
