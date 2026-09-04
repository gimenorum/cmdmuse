package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	APIKey  string
	Model   string
	HTTP    *http.Client
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatReq struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens"`
	Stream      bool      `json:"stream"`
}

type chatResp struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		CompletionTokens int `json:"completion_tokens"`
		PromptTokens     int `json:"prompt_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func New() *Client {
	return &Client{
		BaseURL: strings.TrimRight(envOr("CMDMUSE_BASE_URL", "http://127.0.0.1:18080/v1"), "/"),
		APIKey:  os.Getenv("CMDMUSE_API_KEY"),
		Model:   envOr("CMDMUSE_MODEL", ""),
		HTTP:    &http.Client{Timeout: 120 * time.Second},
	}
}

// Probe は疎通確認を兼ねてモデル名を解決する。Model 未指定なら先頭のモデルを採る。
func (c *Client) Probe(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.BaseURL+"/models", nil)
	if err != nil {
		return err
	}
	c.auth(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s に接続できません: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s が %d を返しました: %s", c.BaseURL, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return err
	}
	if len(out.Data) == 0 {
		return fmt.Errorf("%s にモデルがありません", c.BaseURL)
	}
	if c.Model == "" {
		c.Model = out.Data[0].ID
	}
	return nil
}

func (c *Client) auth(req *http.Request) {
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
}

func (c *Client) Chat(ctx context.Context, msgs []Message, temp float64, maxTok int) (string, error) {
	payload, err := json.Marshal(chatReq{
		Model: c.Model, Messages: msgs, Temperature: temp, MaxTokens: maxTok,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	c.auth(req)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var out chatResp
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("応答を解釈できません (%d): %s", resp.StatusCode, truncate(string(body), 200))
	}
	if out.Error != nil {
		return "", fmt.Errorf("%s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("応答が空です (%d)", resp.StatusCode)
	}
	content := out.Choices[0].Message.Content
	if strings.TrimSpace(content) == "" {
		// 空応答は原因が分からないと直せない。打ち切り理由とトークン数を添える。
		return "", fmt.Errorf("本文が空です (finish_reason=%s, prompt=%d, completion=%d, max_tokens=%d)",
			out.Choices[0].FinishReason, out.Usage.PromptTokens,
			out.Usage.CompletionTokens, maxTok)
	}
	return content, nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
