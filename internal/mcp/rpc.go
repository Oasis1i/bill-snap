package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/Oasis1i/bill-snap/internal/store"
	"github.com/Oasis1i/bill-snap/internal/version"
)

const (
	codeParse    = -32700
	codeBadReq   = -32600
	codeMethod   = -32601
	codeInternal = -32603
)

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

// handleMessage runs one JSON-RPC message.
// notification is true when the client sent no id, so the caller writes nothing.
// status is the HTTP status. The stdio transport ignores it and still writes
// protocol errors so a bad line does not kill the process.
func handleMessage(ctx context.Context, st *store.Store, loc *time.Location, uploadDir string, line []byte) (body []byte, status int, notification bool) {
	if loc == nil {
		loc = time.UTC
	}
	if !json.Valid(line) {
		return rpcFail(json.RawMessage("null"), codeParse, "请求无法解析"), http.StatusBadRequest, false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil || raw == nil {
		return rpcFail(json.RawMessage("null"), codeBadReq, "请求格式不对"), http.StatusBadRequest, false
	}
	id, hasID := raw["id"]
	if !hasID {
		return nil, http.StatusAccepted, true
	}
	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(line, &req); err != nil {
		return rpcFail(id, codeParse, "请求无法解析"), http.StatusBadRequest, false
	}
	if (req.JSONRPC != "" && req.JSONRPC != "2.0") || req.Method == "" {
		return rpcFail(id, codeBadReq, "请求格式不对"), http.StatusBadRequest, false
	}
	switch req.Method {
	case "initialize":
		return rpcOK(id, initResult(req.Params)), http.StatusOK, false
	case "ping":
		return rpcOK(id, map[string]any{}), http.StatusOK, false
	case "tools/list":
		return rpcOK(id, map[string]any{"tools": toolDefs()}), http.StatusOK, false
	case "tools/call":
		return rpcOK(id, callTool(ctx, st, loc, uploadDir, req.Params)), http.StatusOK, false
	default:
		return rpcFail(id, codeMethod, "方法不存在"), http.StatusOK, false
	}
}

func rpcOK(id json.RawMessage, result any) []byte {
	b, err := json.Marshal(okEnvelope{JSONRPC: "2.0", ID: id, Result: result})
	if err != nil {
		return rpcFail(id, codeInternal, "服务器出错")
	}
	return b
}

func rpcFail(id json.RawMessage, code int, message string) []byte {
	if len(bytesTrim(id)) == 0 {
		id = json.RawMessage("null")
	}
	b, err := json.Marshal(errEnvelope{JSONRPC: "2.0", ID: id, Error: rpcError{Code: code, Message: message}})
	if err != nil {
		return []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"服务器出错"}}`)
	}
	return b
}

func bytesTrim(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\n' || b[i] == '\t' || b[i] == '\r') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\n' || b[j-1] == '\t' || b[j-1] == '\r') {
		j--
	}
	return b[i:j]
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

func supportedProtocol(v string) bool {
	switch v {
	case "2024-11-05", "2025-03-26", "2025-06-18":
		return true
	default:
		return false
	}
}
