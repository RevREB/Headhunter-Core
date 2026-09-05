// Package webmcp is a minimal client for the Headhunter-WebMCP browser tool — a
// Playwright MCP server (streamable-HTTP transport) that Core drives to open and
// fill job-application forms. Core never submits; it fills what it safely can and
// hands off to a human via the service's noVNC desktop.
package webmcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is one browser session against the Playwright MCP server.
type Client struct {
	endpoint string
	host     string // Host header — the server only accepts localhost (DNS-rebind guard)
	http     *http.Client
	sid      string
	id       int
}

// New returns a client for a Playwright-MCP streamable-HTTP endpoint, e.g.
// http://headhunter-webmcp.headhunter.svc:8931/mcp.
func New(endpoint string) *Client {
	host := "localhost:8931"
	if u, err := url.Parse(endpoint); err == nil && u.Port() != "" {
		host = "localhost:" + u.Port()
	}
	return &Client{endpoint: endpoint, host: host, http: &http.Client{Timeout: 90 * time.Second}}
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResp struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// rpc performs one JSON-RPC call and returns the result (nil for notifications).
func (c *Client) rpc(ctx context.Context, method string, params any, notif bool) (json.RawMessage, error) {
	c.id++
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if !notif {
		msg["id"] = c.id
	}
	if params != nil {
		msg["params"] = params
	}
	body, _ := json.Marshal(msg)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Host = c.host // the server rejects any Host but localhost
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.sid != "" {
		req.Header.Set("mcp-session-id", c.sid)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("mcp-session-id"); sid != "" {
		c.sid = sid
	}
	if resp.StatusCode == http.StatusAccepted || notif {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("webmcp %s: HTTP %d", method, resp.StatusCode)
	}
	// The transport replies as SSE ("event: message\ndata: {json}"); collect the
	// data payload and decode the JSON-RPC envelope.
	var data strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimSpace(line[5:]))
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(data.String()) == "" {
		// The server opens an SSE stream then closes it empty when a tool fails
		// or a navigation never settles (e.g. an anti-bot page that hangs load).
		return nil, fmt.Errorf("webmcp %s: empty response (page may block automation or timed out)", method)
	}
	var r rpcResp
	if err := json.Unmarshal([]byte(data.String()), &r); err != nil {
		return nil, fmt.Errorf("webmcp %s: decode: %w", method, err)
	}
	if r.Error != nil {
		return nil, fmt.Errorf("webmcp %s: %s", method, r.Error.Message)
	}
	return r.Result, nil
}

// Initialize performs the MCP handshake and captures the session id.
func (c *Client) Initialize(ctx context.Context) error {
	_, err := c.rpc(ctx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "headhunter-core", "version": "1"},
	}, false)
	if err != nil {
		return err
	}
	// notifications/initialized (fire-and-forget)
	_, _ = c.rpc(ctx, "notifications/initialized", nil, true)
	return nil
}

// CallTool invokes an MCP tool and returns its text content.
func (c *Client) CallTool(ctx context.Context, name string, args any) (string, error) {
	res, err := c.rpc(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, false)
	if err != nil {
		return "", err
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, ct := range out.Content {
		if ct.Type == "text" {
			b.WriteString(ct.Text)
		}
	}
	if out.IsError {
		return b.String(), fmt.Errorf("webmcp tool %s reported an error", name)
	}
	return b.String(), nil
}

// Navigate opens a URL in the browser.
func (c *Client) Navigate(ctx context.Context, u string) error {
	_, err := c.CallTool(ctx, "browser_navigate", map[string]any{"url": u})
	return err
}

// Snapshot returns the page's accessibility snapshot (with element refs).
func (c *Client) Snapshot(ctx context.Context) (string, error) {
	return c.CallTool(ctx, "browser_snapshot", map[string]any{})
}

// FillField is one field for FillForm.
type FillField struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Ref   string `json:"ref"`
	Value string `json:"value"`
}

// FillForm fills multiple fields in one call. It never submits.
func (c *Client) FillForm(ctx context.Context, fields []FillField) error {
	if len(fields) == 0 {
		return nil
	}
	_, err := c.CallTool(ctx, "browser_fill_form", map[string]any{"fields": fields})
	return err
}

// Click clicks an element (used to reach an application form behind an "Apply"
// button). element is a human-readable label; ref comes from the snapshot.
func (c *Client) Click(ctx context.Context, element, ref string) error {
	_, err := c.CallTool(ctx, "browser_click", map[string]any{"element": element, "ref": ref})
	return err
}

// WaitTime waits the given seconds for a form to render after a click/navigation.
func (c *Client) WaitTime(ctx context.Context, secs float64) error {
	_, err := c.CallTool(ctx, "browser_wait_for", map[string]any{"time": secs})
	return err
}
