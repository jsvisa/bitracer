package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jsvisa/bitracer/internal/btc"
)

const ExplorerTxBase = "https://mempool.space/tx/"

func ExplorerTxURL(txid string) string { return ExplorerTxBase + txid }

// DashboardBase is the dashboard's public base URL (BITRACER_PUBLIC_URL);
// when set, channels that cannot carry the case-graph image include a
// link to the case's graph view instead.
var DashboardBase string

var hexTxRe = regexp.MustCompile(`\b[0-9a-fA-F]{64}\b`)

// shortLabel renders a txid as head...tail (f639...5b80).
func shortLabel(txid string) string {
	if len(txid) <= 12 {
		return txid
	}
	return txid[:4] + "..." + txid[len(txid)-4:]
}

// linkTx renders a txid as a mempool.space link with a short label.
func linkTx(txid string) string {
	return "<" + ExplorerTxURL(txid) + "|" + shortLabel(txid) + ">"
}

// Holding is one address's share of the case's parked funds.
type Holding struct {
	Address string
	Sats    int64
}

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
	// Parking is where the tracked funds sit at alert time, sorted by
	// amount desc. Optional; renderers show it as a summary line.
	Parking []Holding
	// PNG is an optional image (the case's fund-flow graph); channels
	// that can carry images attach it, the rest render text only.
	PNG []byte
}

func TestMessage() Message {
	return Message{Kind: "test", Headline: "test notification — bitracer can reach this channel"}
}

// Plain renders the flat one-liner stored in the alerts table.
func (m Message) Plain() string {
	return fmt.Sprintf("[bitracer] case#%d: %s", m.CaseID, m.Headline)
}

// parkingLine summarizes where the funds are parked: total plus the top
// few addresses, e.g. "1.80 BTC total — 0.90 at <addr>, 0.60 at <addr>, +2 more".
func (m Message) parkingLine() string {
	if len(m.Parking) == 0 {
		return ""
	}
	var total int64
	for _, h := range m.Parking {
		total += h.Sats
	}
	const maxAddrs = 3
	parts := make([]string, 0, maxAddrs+1)
	for i, h := range m.Parking {
		if i == maxAddrs {
			parts = append(parts, fmt.Sprintf("+%d more", len(m.Parking)-maxAddrs))
			break
		}
		parts = append(parts, fmt.Sprintf("%.2f at %s", btc.SatsToBTC(h.Sats), h.Address))
	}
	return fmt.Sprintf("%.2f BTC total — %s", btc.SatsToBTC(total), strings.Join(parts, ", "))
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
		add("value", fmt.Sprintf("%.2f BTC", btc.SatsToBTC(m.ValueSats)))
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
	add("parking", m.parkingLine())
	return f
}

// graphLink returns the dashboard URL for the case's fund-flow graph,
// included whenever the receiving channel will not actually carry the
// rendered PNG (no image support, missing credentials, or a failed
// render). deliversImage reports whether this send really attaches it.
func (m Message) graphLink(deliversImage bool) string {
	if DashboardBase == "" || m.CaseID <= 0 || deliversImage {
		return ""
	}
	return strings.TrimRight(DashboardBase, "/") + "/#case=" + strconv.FormatInt(m.CaseID, 10) + "&tab=graph"
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
			Token   string `json:"token"`
			Channel string `json:"channel"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("slack config: %w", err)
		}
		if cfg.Webhook == "" && cfg.Token == "" {
			return nil, fmt.Errorf("slack config requires webhook (or a bot token for image posts)")
		}
		return NewSlack(cfg.Webhook, cfg.Token, cfg.Channel), nil
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
			Webhook   string `json:"webhook"`
			AppID     string `json:"app_id"`
			AppSecret string `json:"app_secret"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("lark config: %w", err)
		}
		if cfg.Webhook == "" {
			return nil, fmt.Errorf("lark config requires webhook")
		}
		return NewLark(cfg.Webhook, cfg.AppID, cfg.AppSecret), nil
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
// and a mempool.space link on the txhash. With a bot token configured,
// case-graph images are uploaded via the Web API and posted with the
// text as the file comment; without one, messages go through the
// incoming webhook text-only. channel overrides the target channel
// (a channel ID like C0123456789 with a token, or a #name for legacy
// incoming webhooks).
type Slack struct {
	webhook string
	token   string
	channel string
}

func NewSlack(webhook, token, channel string) *Slack {
	return &Slack{webhook: webhook, token: token, channel: channel}
}

func (s *Slack) Name() string { return "slack" }

func (s *Slack) Send(ctx context.Context, msg Message) error {
	if len(msg.PNG) > 0 && s.token != "" {
		if err := s.sendImage(ctx, msg); err != nil {
			slog.Warn("slack image post failed; falling back to webhook", "err", err)
		} else {
			return nil
		}
	}
	return s.sendWebhook(ctx, msg)
}

// slackText renders the mrkdwn body shared by the webhook card and the
// image-file comment: headline (txids linked) plus labeled fields.
// deliversImage is false for the webhook path, which cannot carry the
// graph image — those get a dashboard link instead.
func slackText(msg Message, deliversImage bool) string {
	var b strings.Builder
	for _, f := range msg.fields() {
		v := f[1]
		if f[0] == "address" {
			v = "`" + v + "`"
		}
		fmt.Fprintf(&b, "*%s:* %s\n", f[0], v)
	}
	headline := msg.Headline
	if headline == "" {
		headline = msg.Plain()
	}
	// Only add a separate tx field when the headline does not already
	// reference this message's txid (full or short form).
	if msg.Txid != "" && !strings.Contains(headline, msg.Txid) && !strings.Contains(headline, shortTx(msg.Txid)) {
		fmt.Fprintf(&b, "*tx:* %s\n", linkTx(msg.Txid))
	}
	if u := msg.graphLink(deliversImage); u != "" {
		fmt.Fprintf(&b, "*graph:* <%s|view fund-flow graph>\n", u)
	}
	// Collapse every full txid in the headline to a linked head...tail form.
	headline = hexTxRe.ReplaceAllStringFunc(headline, func(m string) string {
		return linkTx(strings.ToLower(m))
	})
	return strings.TrimSpace(headline + "\n" + b.String())
}

func (s *Slack) sendWebhook(ctx context.Context, msg Message) error {
	text := slackText(msg, false)
	color := "#bf616a"
	if IsTerminalKind(msg.Kind) {
		color = "#a3be8c"
	}
	// No payload-level text: Slack renders it above the card and mashes it
	// into notifications; the blocks alone carry the whole message.
	blocks := []any{
		map[string]any{
			"type": "header",
			"text": map[string]string{"type": "plain_text", "text": truncate("bitracer · "+kindLabel(msg), 140)},
		},
		map[string]any{
			"type": "section",
			"text": map[string]string{"type": "mrkdwn", "text": text},
		},
	}
	payload := map[string]any{
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
	if len(msg.PNG) > 0 {
		if err := t.sendDocument(ctx, msg); err == nil {
			return nil
		} else {
			slog.Warn("telegram sendDocument failed; sending text", "err", err)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<b>bitracer · %s</b>\n", kindLabel(msg))
	fmt.Fprintf(&b, "<b>%s</b>\n", escapeHTML(msg.Headline))
	for _, f := range msg.fields() {
		fmt.Fprintf(&b, "<b>%s:</b> %s\n", escapeHTML(f[0]), escapeHTML(f[1]))
	}
	if msg.Txid != "" {
		fmt.Fprintf(&b, "<b>tx:</b> <a href=\"%s\">%s</a>\n", ExplorerTxURL(msg.Txid), shortTx(msg.Txid))
	}
	if u := msg.graphLink(false); u != "" {
		fmt.Fprintf(&b, "<b>graph:</b> <a href=\"%s\">view fund-flow graph</a>\n", u)
	}
	return postJSON(ctx, "https://api.telegram.org/bot"+t.token+"/sendMessage",
		map[string]string{"chat_id": t.chat, "text": strings.TrimSpace(b.String()), "parse_mode": "HTML"})
}

// Lark renders an interactive card with markdown elements and a
// mempool.space link. With app_id/app_secret configured, case-graph
// images are uploaded to Lark and embedded in the card; without them,
// cards are text-only.
type Lark struct {
	webhook   string
	appID     string
	appSecret string
}

func NewLark(webhook, appID, appSecret string) *Lark {
	return &Lark{webhook: webhook, appID: appID, appSecret: appSecret}
}

func (l *Lark) Name() string { return "lark" }

func (l *Lark) Send(ctx context.Context, msg Message) error {
	imgKey := ""
	if len(msg.PNG) > 0 && l.appID != "" && l.appSecret != "" {
		key, err := l.uploadImage(ctx, msg.PNG)
		if err == nil {
			imgKey = key
		} else {
			slog.Warn("lark image upload failed; sending text card", "err", err)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**%s**\n", msg.Headline)
	for _, f := range msg.fields() {
		fmt.Fprintf(&b, "- **%s:** %s\n", f[0], f[1])
	}
	if msg.Txid != "" {
		fmt.Fprintf(&b, "- **tx:** [%s](%s)\n", shortTx(msg.Txid), ExplorerTxURL(msg.Txid))
	}
	if u := msg.graphLink(imgKey != ""); u != "" {
		fmt.Fprintf(&b, "- **graph:** [view fund-flow graph](%s)\n", u)
	}
	template := "red"
	if IsTerminalKind(msg.Kind) {
		template = "green"
	}
	elements := []any{
		map[string]string{"tag": "markdown", "content": strings.TrimSpace(b.String())},
	}
	if imgKey != "" {
		elements = append(elements, map[string]any{
			"tag":     "img",
			"img_key": imgKey,
			"alt":     map[string]string{"tag": "plain_text", "content": "case graph"},
		})
	}
	elements = append(elements,
		map[string]any{"tag": "hr"},
		map[string]string{"tag": "plain_text", "content": "sent by bitracer"},
	)
	payload := map[string]any{
		"msg_type": "interactive",
		"card": map[string]any{
			"header": map[string]any{
				"title":    map[string]string{"tag": "plain_text", "content": truncate("bitracer · "+kindLabel(msg), 100)},
				"template": template,
			},
			"elements": elements,
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
