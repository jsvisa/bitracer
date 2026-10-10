package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
)

// sendPhoto posts the graph image with the text as its caption
// (Telegram caps captions at 1024 chars).
func (t *Telegram) sendPhoto(ctx context.Context, msg Message) error {
	var b strings.Builder
	fmt.Fprintf(&b, "<b>bitracer · %s</b>\n", kindLabel(msg))
	fmt.Fprintf(&b, "<b>%s</b>\n", escapeHTML(msg.Headline))
	for _, f := range msg.fields() {
		fmt.Fprintf(&b, "<b>%s:</b> %s\n", escapeHTML(f[0]), escapeHTML(f[1]))
	}
	caption := strings.TrimSpace(b.String())
	if len(caption) > 1024 {
		caption = caption[:1021] + "..."
	}
	body, ctype, err := multipartBody(map[string]string{
		"chat_id":    t.chat,
		"parse_mode": "HTML",
		"caption":    caption,
	}, "photo", "case-graph.png", msg.PNG)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.telegram.org/bot"+t.token+"/sendPhoto", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", ctype)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("telegram sendPhoto http %d", resp.StatusCode)
	}
	var out struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("telegram sendPhoto rejected")
	}
	return nil
}

// sendImage uploads the graph via the Slack Web API files flow and posts
// it to the channel with the text as the file comment. Requires a bot
// token (xoxb-...) with files:write and chat:write scopes; incoming
// webhooks cannot carry files.
func (s *Slack) sendImage(ctx context.Context, msg Message) error {
	// 1. ask Slack for a pre-signed upload URL
	var up struct {
		OK        bool   `json:"ok"`
		UploadURL string `json:"upload_url"`
		FileID    string `json:"file_id"`
		Error     string `json:"error"`
	}
	if err := slackAPI(ctx, s.token, "files.getUploadURLExternal",
		map[string]string{
			"filename": "case-graph.png",
			"length":   fmt.Sprintf("%d", len(msg.PNG)),
		}, &up); err != nil {
		return err
	}
	// 2. push the bytes to it
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, up.UploadURL, bytes.NewReader(msg.PNG))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "image/png")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("slack upload http %d", resp.StatusCode)
	}
	// 3. finalize: this is the step that actually posts the file
	form := map[string]string{
		"files":           fmt.Sprintf(`[{"id":%q,"title":"case graph"}]`, up.FileID),
		"initial_comment": slackText(msg),
	}
	if s.channel != "" {
		form["channels"] = s.channel
	}
	var done struct {
		OK bool `json:"ok"`
	}
	return slackAPI(ctx, s.token, "files.completeUploadExternal", form, &done)
}

// slackAPI posts a form-encoded Web API call and decodes the response,
// mapping Slack's {"ok":false,"error":...} envelope to an error.
func slackAPI(ctx context.Context, token, method string, form map[string]string, out any) error {
	vals := url.Values{}
	for k, v := range form {
		vals.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://slack.com/api/"+method, strings.NewReader(vals.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("slack %s http %d", method, resp.StatusCode)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return err
	}
	var env struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return err
	}
	if !env.OK {
		return fmt.Errorf("slack %s: %s", method, env.Error)
	}
	return nil
}

// uploadImage exchanges app credentials for a tenant_access_token and
// uploads the PNG to Lark's image store, returning its image_key.
func (l *Lark) uploadImage(ctx context.Context, png []byte) (string, error) {
	tok, err := l.tenantToken(ctx)
	if err != nil {
		return "", err
	}
	body, ctype, err := multipartBody(nil, "image", "case-graph.png", png)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://open.feishu.cn/open-apis/im/v1/images?image_type=message", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			ImageKey string `json:"image_key"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Code != 0 {
		return "", fmt.Errorf("lark image upload: code %d %s", out.Code, out.Msg)
	}
	if out.Data.ImageKey == "" {
		return "", fmt.Errorf("lark image upload: empty image_key")
	}
	return out.Data.ImageKey, nil
}

func (l *Lark) tenantToken(ctx context.Context) (string, error) {
	var out struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
	}
	if err := postJSONResp(ctx, "https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal",
		map[string]string{"app_id": l.appID, "app_secret": l.appSecret}, &out); err != nil {
		return "", err
	}
	if out.Code != 0 || out.TenantAccessToken == "" {
		return "", fmt.Errorf("lark tenant token: code %d %s", out.Code, out.Msg)
	}
	return out.TenantAccessToken, nil
}

// postJSONResp posts a JSON payload and decodes the response into out.
func postJSONResp(ctx context.Context, url string, payload, out any) error {
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
	return json.NewDecoder(resp.Body).Decode(out)
}

// multipartBody assembles a multipart/form-data body with the given text
// fields and one binary file field.
func multipartBody(fields map[string]string, fileField, filename string, data []byte) ([]byte, string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return nil, "", err
		}
	}
	fw, err := mw.CreateFormFile(fileField, filename)
	if err != nil {
		return nil, "", err
	}
	if _, err := fw.Write(data); err != nil {
		return nil, "", err
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), mw.FormDataContentType(), nil
}
