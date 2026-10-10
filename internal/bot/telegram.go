// Package bot implements the two-way Telegram assistant: it long-polls
// Telegram for questions, answers them via an OpenAI-compatible LLM with
// tool access to the bitracer store, and optionally performs write
// actions for admin chats.
package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jsvisa/bitracer/internal/httpx"
)

const telegramAPI = "https://api.telegram.org"

// Telegram is a minimal Telegram Bot API client: getUpdates long-polling
// plus sendMessage. No webhook, no public URL needed.
type Telegram struct {
	token string
	hc    *http.Client
}

func NewTelegram(token string) *Telegram {
	return &Telegram{token: token, hc: &http.Client{Timeout: 40 * time.Second}}
}

type tgUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type tgChat struct {
	ID int64 `json:"id"`
}

type tgMessage struct {
	MessageID int64   `json:"message_id"`
	Text      string  `json:"text"`
	Chat      tgChat  `json:"chat"`
	From      *tgUser `json:"from"`
}

type tgUpdate struct {
	UpdateID int64      `json:"update_id"`
	Message  *tgMessage `json:"message"`
}

type tgResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

// Me returns the bot's own identity (username used for @mention detection).
func (t *Telegram) Me(ctx context.Context) (tgUser, error) {
	var u tgUser
	err := t.get(ctx, "/getMe", &u)
	return u, err
}

// Updates long-polls getUpdates for up to waitSec seconds and returns the
// new updates. offset is the lowest update_id still to be fetched.
func (t *Telegram) Updates(ctx context.Context, offset int64, waitSec int) ([]tgUpdate, error) {
	q := url.Values{}
	q.Set("timeout", fmt.Sprintf("%d", waitSec))
	q.Set("offset", fmt.Sprintf("%d", offset))
	q.Set("allowed_updates", `["message"]`)
	var ups []tgUpdate
	if err := t.get(ctx, "/getUpdates?"+q.Encode(), &ups); err != nil {
		return nil, err
	}
	return ups, nil
}

// Send delivers text to chatID, splitting at Telegram's 4096-char limit.
func (t *Telegram) Send(ctx context.Context, chatID int64, text string) error {
	for _, chunk := range splitChunks(text, 4000) {
		payload := map[string]any{
			"chat_id":                  chatID,
			"text":                     chunk,
			"disable_web_page_preview": true,
		}
		if err := t.post(ctx, "/sendMessage", payload); err != nil {
			return err
		}
	}
	return nil
}

// Typing flashes the "typing..." indicator so users know the bot is on it.
func (t *Telegram) Typing(ctx context.Context, chatID int64) {
	_ = t.post(ctx, "/sendChatAction", map[string]any{"chat_id": chatID, "action": "typing"})
}

func (t *Telegram) get(ctx context.Context, path string, out any) error {
	return t.call(ctx, http.MethodGet, path, nil, out)
}

func (t *Telegram) post(ctx context.Context, path string, payload any) error {
	return t.call(ctx, http.MethodPost, path, payload, nil)
}

func (t *Telegram) call(ctx context.Context, method, path string, payload, out any) error {
	status, raw, err := httpx.Request{
		Method: method,
		URL:    telegramAPI + "/bot" + t.token + path,
		Body:   payload,
	}.Do(ctx, t.hc)
	if err != nil {
		return err
	}
	var r tgResponse
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("telegram http %d: %w", status, err)
	}
	if !r.OK {
		return fmt.Errorf("telegram: %s", r.Description)
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

func splitChunks(s string, n int) []string {
	if len(s) <= n {
		return []string{s}
	}
	var out []string
	for len(s) > n {
		cut := strings.LastIndex(s[:n], "\n")
		if cut < n/2 {
			cut = n
		}
		// Never split a UTF-8 rune: back off to the rune boundary.
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		out = append(out, s[:cut])
		s = strings.TrimPrefix(s[cut:], "\n")
	}
	return append(out, s)
}
