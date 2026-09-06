package api

// Read-only MCP front door for Headhunter. Speaks JSON-RPC 2.0 over the MCP
// streamable-HTTP transport (POST /mcp): a request gets a single JSON response;
// a notification gets 202 with no body. Registered in Bifrost as "headhunter"
// so Eva can discover + call these tools through the gateway without a config
// change on her side. Read-only: no cycle/pause/dedup/requeue here — sweeps are
// configured in the Config screen, and discard/apply stay the user's calls.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"
)

const mcpProtocolVersion = "2025-06-18"

type mcpReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // absent/null => notification
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}
type mcpErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type mcpResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *mcpErr         `json:"error,omitempty"`
}

func newMCPSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// mcp is the JSON-RPC endpoint. GET is rejected (no server-initiated stream is
// needed for a request/response tool server).
func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	var req mcpReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, mcpResp{JSONRPC: "2.0", Error: &mcpErr{Code: -32700, Message: "parse error"}})
		return
	}
	if len(req.ID) == 0 || string(req.ID) == "null" { // notification: accept, no body
		w.WriteHeader(http.StatusAccepted)
		return
	}
	resp := mcpResp{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", newMCPSessionID())
		resp.Result = map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "headhunter", "version": "1"},
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": mcpTools()}
	case "tools/call":
		resp.Result, resp.Error = s.mcpCallTool(r.Context(), req.Params)
	default:
		resp.Error = &mcpErr{Code: -32601, Message: "method not found: " + req.Method}
	}
	writeJSON(w, http.StatusOK, resp)
}

func mcpTools() []map[string]any {
	obj := func(props map[string]any, required ...string) map[string]any {
		m := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			m["required"] = required
		}
		return m
	}
	num := func(d string) map[string]any { return map[string]any{"type": "number", "description": d} }
	str := func(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
	intg := func(d string) map[string]any { return map[string]any{"type": "integer", "description": d} }
	return []map[string]any{
		{
			"name": "list_roles",
			"description": "List roles from the pipeline, filtered and highest-fit first. For the hourly high-fit watch, call " +
				"list_roles(status=\"evaluated\", min_score=4.0, since=<RFC3339 of your last check>): returns roles scored at/above " +
				"min_score that were evaluated at or after `since`, so you only see NEW high-fit roles. Each row has id, company, " +
				"role, score (0–5; 3.0 is the apply line), status, url, and evaluated_at.",
			"inputSchema": obj(map[string]any{
				"status":    str("filter by pipeline status (e.g. evaluated, applied). Omit for any."),
				"min_score": num("only roles scored >= this, on a 0–5 scale (the apply line is 3.0)."),
				"since":     str("RFC3339 timestamp; only roles evaluated at or after this. Pass your last-check time to get only new roles."),
				"limit":     intg("max rows (default 50, max 500)."),
			}),
		},
		{
			"name":        "get_role",
			"description": "Full detail for one role by id: the scraped posting, the A–G evaluation report, and the status history. Use it to explain WHY a role scored the way it did and to get the apply URL.",
			"inputSchema": obj(map[string]any{"id": intg("application id")}, "id"),
		},
		{"name": "funnel_stats", "description": "Pipeline counts by status (inbox, evaluated, applied, …) plus a total. Canonical-deduped.", "inputSchema": obj(map[string]any{})},
		{"name": "eval_status", "description": "Evaluator health: enabled, workers, queue depth (waiting), in-flight, and this session's done/failed counts.", "inputSchema": obj(map[string]any{})},
		{"name": "scan_status", "description": "Scan/operator status: last run and the live state of the current scan jobs.", "inputSchema": obj(map[string]any{})},
	}
}

// mcpCallTool dispatches a tools/call. Returns an MCP tool result (a content
// array) or a JSON-RPC error for protocol-level failures (bad params, unknown
// tool). Tool-level failures are returned as an isError result, per MCP.
func (s *Server) mcpCallTool(ctx context.Context, params json.RawMessage) (any, *mcpErr) {
	var p struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &mcpErr{Code: -32602, Message: "invalid params"}
	}
	if s.Store == nil {
		return mcpText("", "no database", true), nil
	}
	var data any
	var err error
	switch p.Name {
	case "list_roles":
		data, err = s.mcpListRoles(ctx, p.Args)
	case "get_role":
		data, err = s.mcpGetRole(ctx, p.Args)
	case "funnel_stats":
		if s.Analytics == nil {
			return mcpText("", "no analytics", true), nil
		}
		data, err = s.Analytics.Funnel(ctx)
	case "eval_status":
		data = s.mcpEvalStatus(ctx)
	case "scan_status":
		if s.Operator == nil {
			data = map[string]any{"operator": false, "running": false, "jobs": []any{}}
		} else {
			data, err = s.Operator.Status(ctx)
		}
	default:
		return nil, &mcpErr{Code: -32602, Message: "unknown tool: " + p.Name}
	}
	if err != nil {
		return mcpText("", err.Error(), true), nil
	}
	js, _ := json.Marshal(data)
	return mcpText(string(js), "", false), nil
}

// mcpText builds an MCP tool result carrying JSON (or an error message) in a
// single text content block.
func mcpText(js, errMsg string, isErr bool) map[string]any {
	txt := js
	if isErr {
		txt = "error: " + errMsg
	}
	out := map[string]any{"content": []map[string]any{{"type": "text", "text": txt}}}
	if isErr {
		out["isError"] = true
	}
	return out
}

func (s *Server) mcpListRoles(ctx context.Context, args json.RawMessage) (any, error) {
	var a struct {
		Status   string   `json:"status"`
		MinScore *float64 `json:"min_score"`
		Since    string   `json:"since"`
		Limit    int      `json:"limit"`
	}
	_ = json.Unmarshal(args, &a)
	minScore := -1.0
	if a.MinScore != nil {
		minScore = *a.MinScore
	}
	var since *time.Time
	if a.Since != "" {
		if t, e := time.Parse(time.RFC3339, a.Since); e == nil {
			since = &t
		}
	}
	rows, err := s.Store.RolesForMCP(ctx, a.Status, minScore, since, a.Limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"roles": rows, "count": len(rows)}, nil
}

func (s *Server) mcpGetRole(ctx context.Context, args json.RawMessage) (any, error) {
	var a struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(args, &a)
	return s.Store.GetApplication(ctx, a.ID)
}

func (s *Server) mcpEvalStatus(ctx context.Context) map[string]any {
	enabled, workers := s.evalSettings(ctx)
	waiting, claimed, _ := s.Store.QueueStats(ctx)
	return map[string]any{
		"enabled": enabled, "workers": workers,
		"waiting": waiting, "claimed": claimed,
		"inFlight":      int(atomic.LoadInt64(&s.evalInFlight)),
		"doneSession":   atomic.LoadInt64(&s.evalDone),
		"failedSession": atomic.LoadInt64(&s.evalFailed),
		"llm":           s.LLM != nil,
	}
}
