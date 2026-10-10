package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jsvisa/bitracer/internal/config"
	"github.com/jsvisa/bitracer/internal/store"
)

var errAdminOnly = errors.New("this action is only available to admin chats")

// answerTimeout bounds one question's whole LLM tool loop.
const answerTimeout = 3 * time.Minute

// Bot answers Telegram questions about bitracer state via an LLM with
// tool access to the store. Read questions are answered in any allowed
// chat; write actions only in admin chats.
type Bot struct {
	st         *store.Store
	llm        *LLM
	tg         *Telegram
	readAllow  map[int64]bool
	adminAllow map[int64]bool
	username   string // bot's own @username, "" when getMe failed
	offset     int64
}

// Enabled reports whether the bot has its two required configs.
func Enabled(cfg config.Config) bool {
	return cfg.BotTelegramToken != "" && cfg.BotLLMKey != ""
}

// New wires the bot; allowChat falls back to the chat_ids of configured
// telegram notify channels when BITRACER_BOT_TELEGRAM_CHATS is unset.
func New(ctx context.Context, st *store.Store, cfg config.Config) *Bot {
	b := &Bot{
		st:         st,
		llm:        NewLLM(cfg.BotLLMURL, cfg.BotLLMKey, cfg.BotLLMModel),
		tg:         NewTelegram(cfg.BotTelegramToken),
		readAllow:  parseChatIDs(cfg.BotTelegramChats),
		adminAllow: parseChatIDs(cfg.BotAdminChats),
	}
	if len(b.readAllow) == 0 {
		for _, id := range b.telegramChannelChats(ctx) {
			b.readAllow[id] = true
		}
	}
	if len(b.readAllow) == 0 {
		slog.Warn("bot: no allowed chats (set BITRACER_BOT_TELEGRAM_CHATS or configure a telegram channel); questions will be ignored")
	} else {
		ids := make([]string, 0, len(b.readAllow))
		for id := range b.readAllow {
			ids = append(ids, strconv.FormatInt(id, 10))
		}
		slog.Info("bot: answering chats", "chats", ids)
	}
	if len(b.adminAllow) == 0 {
		slog.Warn("bot: write tools disabled (BITRACER_BOT_ADMIN_CHATS empty)")
	}
	if u, err := b.tg.Me(ctx); err == nil {
		b.username = u.Username
		slog.Info("bot: identity", "username", "@"+u.Username)
	} else {
		slog.Warn("bot: getMe failed; in groups every received message will be answered", "err", err)
	}
	return b
}

// Run long-polls Telegram until the context is cancelled. Questions are
// answered concurrently across chats but serialized within one chat via a
// FIFO worker per chat, so replies keep their order.
func (b *Bot) Run(ctx context.Context) error {
	b.skipBacklog(ctx)
	var mu sync.Mutex
	workers := map[int64]chan *tgMessage{}
	workerFor := func(chat int64) chan *tgMessage {
		mu.Lock()
		defer mu.Unlock()
		w, ok := workers[chat]
		if !ok {
			w = make(chan *tgMessage, chatQueueLen)
			go b.chatWorker(ctx, w)
			workers[chat] = w
		}
		return w
	}
	for {
		ups, err := b.tg.Updates(ctx, b.offset+1, 25)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Error("bot: getUpdates failed", "err", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(5 * time.Second):
			}
			continue
		}
		for _, u := range ups {
			if u.UpdateID > b.offset {
				b.offset = u.UpdateID
			}
			if u.Message == nil || strings.TrimSpace(u.Message.Text) == "" {
				continue
			}
			select {
			case workerFor(u.Message.Chat.ID) <- u.Message:
			default:
				slog.Warn("bot: chat queue full, dropping message", "chat_id", u.Message.Chat.ID)
			}
		}
	}
}

// chatQueueLen bounds how many questions may wait in one chat before new
// ones are dropped instead of piling up behind a stuck LLM.
const chatQueueLen = 32

// chatWorker drains one chat's queue in arrival order. A panic in handling
// must not take down serve.
func (b *Bot) chatWorker(ctx context.Context, ch chan *tgMessage) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-ch:
			mctx, cancel := context.WithTimeout(ctx, answerTimeout)
			func() {
				defer cancel()
				defer func() {
					if p := recover(); p != nil {
						slog.Error("bot: panic handling message", "chat_id", m.Chat.ID, "panic", p)
					}
				}()
				b.handle(mctx, m)
			}()
		}
	}
}

// skipBacklog advances the update offset past everything queued before
// startup, so a fresh deploy (or one that was down for a while) does not
// replay stale questions — possibly re-running write actions.
func (b *Bot) skipBacklog(ctx context.Context) {
	ups, err := b.tg.Updates(ctx, -1, 0)
	if err != nil {
		slog.Warn("bot: backlog check failed; consuming from the oldest queued update", "err", err)
		return
	}
	if len(ups) > 0 {
		b.offset = ups[len(ups)-1].UpdateID
		slog.Info("bot: skipping queued backlog", "last_update_id", b.offset)
	}
}

func (b *Bot) handle(ctx context.Context, m *tgMessage) {
	if !b.readAllow[m.Chat.ID] {
		slog.Warn("bot: ignoring disallowed chat", "chat_id", m.Chat.ID)
		return
	}
	text := strings.TrimSpace(m.Text)
	// Groups (chat id < 0): only answer messages that @mention the bot,
	// regardless of Telegram privacy mode, and strip the mention before
	// the LLM sees it.
	if m.Chat.ID < 0 && b.username != "" {
		if !strings.Contains(strings.ToLower(text), "@"+strings.ToLower(b.username)) {
			return
		}
		text = stripMention(text, b.username)
		if text == "" {
			return
		}
	}
	admin := b.adminAllow[m.Chat.ID]
	b.tg.Typing(ctx, m.Chat.ID)

	reply, err := b.answer(ctx, m.Chat.ID, text, admin)
	if err != nil {
		slog.Error("bot: answer failed", "chat_id", m.Chat.ID, "err", err)
		reply = "sorry, answering failed: " + err.Error()
	}
	if strings.TrimSpace(reply) == "" {
		reply = "(no answer)"
	}
	if err := b.tg.Send(ctx, m.Chat.ID, reply); err != nil {
		slog.Error("bot: send failed", "chat_id", m.Chat.ID, "err", err)
	}
}

const maxToolRounds = 6

// answer runs the LLM tool loop for one user question. Conversations are
// stateless per message: every question carries the full system context.
func (b *Bot) answer(ctx context.Context, chatID int64, text string, admin bool) (string, error) {
	if strings.HasPrefix(strings.TrimSpace(text), "/start") {
		return helpText(admin), nil
	}
	msgs := []ChatMessage{
		{Role: "system", Content: b.systemPrompt(ctx)},
		{Role: "user", Content: text},
	}
	tools := toolsFor(admin)
	for round := 0; round < maxToolRounds; round++ {
		resp, err := b.llm.Chat(ctx, msgs, tools)
		if err != nil {
			return "", err
		}
		msgs = append(msgs, resp)
		if len(resp.ToolCalls) == 0 {
			return resp.Content, nil
		}
		for _, tc := range resp.ToolCalls {
			out := b.dispatch(ctx, tc.Function.Name, tc.Function.Arguments, chatID, admin)
			msgs = append(msgs, ChatMessage{Role: "tool", Content: out, ToolCallID: tc.ID})
		}
	}
	return "", fmt.Errorf("tool loop did not converge after %d rounds", maxToolRounds)
}

func (b *Bot) systemPrompt(ctx context.Context) string {
	var sb strings.Builder
	sb.WriteString(`You are the bitracer assistant inside the alert chat channels of a
stolen-Bitcoin fund tracking system. bitracer indexes the Bitcoin chain from a
full node, tracks stolen seed transactions hop-by-hop (depth-first with
fan-out, fan-in and value-decay stopping rules), and posts an alert to these
chat channels whenever tracked funds move or reach a known entity such as an
exchange, mixer, gambling service or darknet market ("terminal" hit).

Use the provided tools to answer questions about cases, where funds are
currently parked, movement history, addresses, transactions and indexer
health. Values from tools are satoshis; present them in BTC
(1 BTC = 100000000 sats). Answers are sent to Telegram as plain text, so do
not use markdown formatting; txids and addresses stay as-is. Be concise and
factual, and say so briefly when a tool returns an error or empty result.
Only call the write tools (create_case, set_case_status, add_case_tx,
mark_tx_terminal, mark_address_terminal, test_channel) when the user
explicitly asks for a change, and confirm what you did afterwards.

`)
	if ss, err := b.st.SyncState(ctx); err == nil {
		fmt.Fprintf(&sb, "Indexer status: last indexed height %d (updated %s).\n",
			ss.LastHeight, ss.UpdatedAt.Format(time.RFC3339))
	}
	if cs, err := b.st.ListCases(ctx); err == nil {
		var ids []string
		for _, c := range cs {
			ids = append(ids, fmt.Sprintf("#%d %s (%s)", c.ID, c.Name, c.Status))
		}
		if len(ids) > 0 {
			sb.WriteString("Known cases: " + strings.Join(ids, "; ") + ".\n")
		}
	}
	sb.WriteString("Today is " + time.Now().Format("2006-01-02") + ".")
	return sb.String()
}

// telegramChannelChats returns the chat_ids of configured telegram notify
// channels — the trusted alert destinations, used as the default read
// allowlist when BITRACER_BOT_TELEGRAM_CHATS is unset.
func (b *Bot) telegramChannelChats(ctx context.Context) []int64 {
	chs, err := b.st.ListChannels(ctx)
	if err != nil {
		return nil
	}
	var out []int64
	for _, ch := range chs {
		if ch.Type != "telegram" {
			continue
		}
		var cfg struct {
			ChatID string `json:"chat_id"`
		}
		if json.Unmarshal(ch.Config, &cfg) != nil || cfg.ChatID == "" {
			continue
		}
		if id, err := strconv.ParseInt(cfg.ChatID, 10, 64); err == nil {
			out = append(out, id)
		}
	}
	return out
}

func helpText(admin bool) string {
	var b strings.Builder
	b.WriteString("bitracer bot — ask me about tracked cases, e.g.\n" +
		"• list cases\n" +
		"• where are case 1's funds parked?\n" +
		"• show case 1's movement history\n" +
		"• what is address bc1q…?\n" +
		"• is tx <txid> indexed?\n" +
		"• indexer status\n")
	if admin {
		b.WriteString("\nYou are an admin and may also ask me to: create a case, " +
			"pause/resume a case, add a seed tx, mark a tx/address terminal, " +
			"or send a test alert.")
	}
	return b.String()
}

// stripMention removes every case-insensitive "@username" occurrence from
// text so the LLM sees the question, not the bot handle.
func stripMention(text, username string) string {
	at := "@" + strings.ToLower(username)
	for {
		low := strings.ToLower(text)
		i := strings.Index(low, at)
		if i < 0 {
			return strings.TrimSpace(text)
		}
		text = text[:i] + text[i+len(at):]
	}
}

func parseChatIDs(s string) map[int64]bool {
	out := map[int64]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if id, err := strconv.ParseInt(part, 10, 64); err == nil {
			out[id] = true
		} else {
			slog.Warn("bot: ignoring non-numeric chat id", "value", part)
		}
	}
	return out
}

func timeOf(ts int64) string {
	if ts == 0 {
		return ""
	}
	return time.Unix(ts, 0).UTC().Format(time.RFC3339)
}
