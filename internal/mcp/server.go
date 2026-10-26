// Package mcp is a newline-delimited JSON-RPC MCP server over stdio.
// It reads the same SQLite database as the HTTP API. It does not check
// UPLOAD_TOKEN: anyone who can spawn the process can already read the file.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/Oasis1i/bill-snap/internal/model"
	"github.com/Oasis1i/bill-snap/internal/store"
	"github.com/Oasis1i/bill-snap/internal/version"
)

// Serve reads MCP messages from r and writes responses to w until r hits EOF.
func Serve(r io.Reader, w io.Writer, st *store.Store, loc *time.Location) error {
	if loc == nil {
		loc = time.UTC
	}
	bw := bufio.NewWriter(w)
	defer bw.Flush()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := handleLine(bw, st, loc, line); err != nil {
			return err
		}
		if err := bw.Flush(); err != nil {
			return err
		}
	}
	return sc.Err()
}

func handleLine(w io.Writer, st *store.Store, loc *time.Location, line []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return writeMsg(w, errEnvelope{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: rpcError{Code: -32700, Message: "parse error"}})
	}
	var req struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(line, &req); err != nil {
		return writeMsg(w, errEnvelope{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: rpcError{Code: -32700, Message: "parse error"}})
	}
	id, hasID := raw["id"]
	if !hasID {
		return nil
	}
	switch req.Method {
	case "initialize":
		return writeMsg(w, okEnvelope{JSONRPC: "2.0", ID: id, Result: initResult(req.Params)})
	case "ping":
		return writeMsg(w, okEnvelope{JSONRPC: "2.0", ID: id, Result: map[string]any{}})
	case "tools/list":
		return writeMsg(w, okEnvelope{JSONRPC: "2.0", ID: id, Result: map[string]any{"tools": toolDefs()}})
	case "tools/call":
		return writeMsg(w, okEnvelope{JSONRPC: "2.0", ID: id, Result: callTool(st, loc, req.Params)})
	default:
		return writeMsg(w, errEnvelope{JSONRPC: "2.0", ID: id, Error: rpcError{Code: -32601, Message: "method not found"}})
	}
}

type okEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result"`
}

type errEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   rpcError        `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func writeMsg(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

func initResult(params json.RawMessage) map[string]any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)
	return map[string]any{
		"protocolVersion": negotiate(p.ProtocolVersion),
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{
			"name":    "bill-snap",
			"version": version.Version,
		},
	}
}

func negotiate(client string) string {
	switch client {
	case "2024-11-05", "2025-03-26", "2025-06-18":
		return client
	default:
		return "2024-11-05"
	}
}

func toolDefs() []map[string]any {
	return []map[string]any{
		{
			"name":        "list_bills",
			"description": "List stored receipt bills. Filter with from/to (YYYY-MM-DD in APP_TZ, or RFC3339) and category (exact match, for example 外卖).",
			"inputSchema": json.RawMessage(`{
				"type": "object",
				"properties": {
					"from": {"type": "string", "description": "Inclusive start. YYYY-MM-DD (APP_TZ) or RFC3339."},
					"to": {"type": "string", "description": "Inclusive end. YYYY-MM-DD (APP_TZ) or RFC3339."},
					"category": {"type": "string", "description": "Exact category match, for example 外卖."},
					"limit": {"type": "integer", "description": "Max rows, 1-500. Default 100."}
				}
			}`),
		},
		{
			"name":        "summarize_spend",
			"description": "Sum spending and break it down by category. Use category 外卖 for questions like how much was spent on takeout this week.",
			"inputSchema": json.RawMessage(`{
				"type": "object",
				"properties": {
					"from": {"type": "string", "description": "Inclusive start. YYYY-MM-DD (APP_TZ) or RFC3339."},
					"to": {"type": "string", "description": "Inclusive end. YYYY-MM-DD (APP_TZ) or RFC3339."},
					"category": {"type": "string", "description": "Exact category match, for example 外卖."}
				}
			}`),
		},
	}
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

func callTool(st *store.Store, loc *time.Location, params json.RawMessage) toolResult {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(params) > 0 && string(params) != "null" {
		if err := json.Unmarshal(params, &call); err != nil {
			return toolError(err.Error())
		}
	}
	if len(call.Arguments) == 0 || string(call.Arguments) == "null" {
		call.Arguments = []byte(`{}`)
	}
	ctx := context.Background()
	switch call.Name {
	case "list_bills":
		var args struct {
			From     string `json:"from"`
			To       string `json:"to"`
			Category string `json:"category"`
			Limit    *int   `json:"limit"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return toolError(err.Error())
		}
		limit, limitSet := 0, false
		if args.Limit != nil {
			limit, limitSet = *args.Limit, true
		}
		start, end, cat, lim, err := model.ResolveQuery(args.From, args.To, args.Category, limit, limitSet, loc)
		if err != nil {
			return toolError(err.Error())
		}
		bills, err := st.List(ctx, store.ListFilter{From: start, To: end, Category: cat, Limit: lim})
		if err != nil {
			return toolError(err.Error())
		}
		return toolJSON(model.BillList{From: args.From, To: args.To, Category: cat, Count: len(bills), Bills: bills})
	case "summarize_spend":
		var args struct {
			From     string `json:"from"`
			To       string `json:"to"`
			Category string `json:"category"`
		}
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return toolError(err.Error())
		}
		start, end, cat, _, err := model.ResolveQuery(args.From, args.To, args.Category, 0, false, loc)
		if err != nil {
			return toolError(err.Error())
		}
		sum, err := st.Summarize(ctx, store.ListFilter{From: start, To: end, Category: cat})
		if err != nil {
			return toolError(err.Error())
		}
		sum.From = strings.TrimSpace(args.From)
		sum.To = strings.TrimSpace(args.To)
		sum.Category = cat
		return toolJSON(sum)
	default:
		return toolError("unknown tool " + call.Name)
	}
}

func toolJSON(v any) toolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return toolError(err.Error())
	}
	return toolResult{Content: []toolContent{{Type: "text", Text: string(b)}}}
}

func toolError(msg string) toolResult {
	return toolResult{IsError: true, Content: []toolContent{{Type: "text", Text: msg}}}
}
