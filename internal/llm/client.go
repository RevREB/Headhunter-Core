// Package llm is a minimal OpenAI-compatible streaming client (for Bifrost).
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Msg is one chat message.
type Msg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Client talks to an OpenAI-compatible chat/completions endpoint.
type Client struct {
	BaseURL string
	Model   string
	Key     string
	HTTP    *http.Client
}

// FromEnv builds a client from OPENAI_BASE_URL/OPENAI_MODEL/OPENAI_API_KEY, or
// returns nil if unconfigured (assistant disabled).
func FromEnv() *Client {
	base, key := os.Getenv("OPENAI_BASE_URL"), os.Getenv("OPENAI_API_KEY")
	if base == "" || key == "" {
		return nil
	}
	model := os.Getenv("OPENAI_MODEL")
	if model == "" {
		model = "guardrails/claude-sonnet-5"
	}
	// A–G report generation (large, non-streaming) regularly runs ~140s through
	// Bifrost; 180s was too tight, so slow-but-valid calls timed out and then
	// burned 3 retries. Default to 300s; override with OPENAI_TIMEOUT_SEC.
	timeout := 300 * time.Second
	if v := os.Getenv("OPENAI_TIMEOUT_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			timeout = time.Duration(n) * time.Second
		}
	}
	return &Client{BaseURL: strings.TrimRight(base, "/"), Model: model, Key: key, HTTP: &http.Client{Timeout: timeout}}
}

// Stream sends messages and invokes onDelta for each streamed content chunk.
func (c *Client) Stream(ctx context.Context, msgs []Msg, onDelta func(string)) error {
	body, _ := json.Marshal(map[string]any{"model": c.Model, "messages": msgs, "stream": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("x-bf-vk", c.Key) // Bifrost extracts the virtual key from this header
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		return fmt.Errorf("llm %d: %s", resp.StatusCode, strings.TrimSpace(b.String()))
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[5:])
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			onDelta(chunk.Choices[0].Delta.Content)
		}
	}
	return sc.Err()
}

// CompleteWith is Complete with an explicit temperature and max_tokens (for the
// long A–G report generation). maxTokens<=0 omits the cap.
func (c *Client) CompleteWith(ctx context.Context, msgs []Msg, temperature float64, maxTokens int) (string, error) {
	req := map[string]any{"model": c.Model, "messages": msgs, "stream": false, "temperature": temperature}
	if maxTokens > 0 {
		req["max_tokens"] = maxTokens
	}
	return c.complete(ctx, req)
}

// Complete sends messages and returns the full assistant reply (non-streaming).
func (c *Client) Complete(ctx context.Context, msgs []Msg) (string, error) {
	return c.complete(ctx, map[string]any{"model": c.Model, "messages": msgs, "stream": false})
}

// CompleteModel is CompleteWith on an explicit model (empty = the client's
// default) — for cheap off-path passes like company-profile synthesis.
func (c *Client) CompleteModel(ctx context.Context, model string, msgs []Msg, temperature float64, maxTokens int) (string, error) {
	if model == "" {
		model = c.Model
	}
	req := map[string]any{"model": model, "messages": msgs, "stream": false, "temperature": temperature}
	if maxTokens > 0 {
		req["max_tokens"] = maxTokens
	}
	return c.complete(ctx, req)
}

func (c *Client) complete(ctx context.Context, reqBody map[string]any) (string, error) {
	body, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("x-bf-vk", c.Key)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("llm: no choices")
	}
	return out.Choices[0].Message.Content, nil
}
