package bot

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

// LLM is a minimal OpenAI-compatible chat-completions client with tool
// calling. Works with OpenAI, DeepSeek, Zhipu, Ollama and any gateway
// speaking the same wire format.
type LLM struct {
	url   string // base, e.g. https://api.openai.com/v1
	key   string
	model string
	hc    *http.Client
}

func NewLLM(url, key, model string) *LLM {
	return &LLM{url: strings.TrimRight(url, "/"), key: key, model: model,
		hc: &http.Client{Timeout: 120 * time.Second}}
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// ChatMessage is one conversation turn; ToolCalls is set on assistant turns
// that request tools, ToolCallID marks tool-result turns.
type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ToolDef struct {
	Type     string   `json:"type"` // always "function"
	Function ToolFunc `json:"function"`
}

type ToolFunc struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
	Tools    []ToolDef     `json:"tools,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message      ChatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Chat sends the conversation and returns the assistant's next message.
func (l *LLM) Chat(ctx context.Context, msgs []ChatMessage, tools []ToolDef) (ChatMessage, error) {
	body, err := json.Marshal(chatRequest{Model: l.model, Messages: msgs, Tools: tools})
	if err != nil {
		return ChatMessage{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.url+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return ChatMessage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if l.key != "" {
		req.Header.Set("Authorization", "Bearer "+l.key)
	}
	resp, err := l.hc.Do(req)
	if err != nil {
		return ChatMessage{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return ChatMessage{}, err
	}
	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return ChatMessage{}, fmt.Errorf("llm http %d: %w", resp.StatusCode, err)
	}
	if cr.Error != nil {
		return ChatMessage{}, fmt.Errorf("llm: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return ChatMessage{}, fmt.Errorf("llm: no choices (http %d)", resp.StatusCode)
	}
	return cr.Choices[0].Message, nil
}
