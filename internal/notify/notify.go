package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

type Notifier interface {
	Name() string
	Send(ctx context.Context, msg string) error
}

func Build(typ string, raw json.RawMessage) (Notifier, error) {
	switch typ {
	case "slack":
		var cfg struct {
			Webhook string `json:"webhook"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("slack config: %w", err)
		}
		if cfg.Webhook == "" {
			return nil, fmt.Errorf("slack config requires webhook")
		}
		return NewSlack(cfg.Webhook), nil
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

func SendAll(ctx context.Context, notifiers []Notifier, msg string) {
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

type Slack struct{ webhook string }

func NewSlack(webhook string) *Slack { return &Slack{webhook: webhook} }

func (s *Slack) Name() string { return "slack" }

func (s *Slack) Send(ctx context.Context, msg string) error {
	return postJSON(ctx, s.webhook, map[string]string{"text": msg})
}

type Telegram struct {
	token string
	chat  string
}

func NewTelegram(token, chat string) *Telegram { return &Telegram{token: token, chat: chat} }

func (t *Telegram) Name() string { return "telegram" }

func (t *Telegram) Send(ctx context.Context, msg string) error {
	return postJSON(ctx, "https://api.telegram.org/bot"+t.token+"/sendMessage",
		map[string]string{"chat_id": t.chat, "text": msg})
}

type Lark struct{ webhook string }

func NewLark(webhook string) *Lark { return &Lark{webhook: webhook} }

func (l *Lark) Name() string { return "lark" }

func (l *Lark) Send(ctx context.Context, msg string) error {
	return postJSON(ctx, l.webhook, map[string]any{
		"msg_type": "text",
		"content":  map[string]string{"text": msg},
	})
}
