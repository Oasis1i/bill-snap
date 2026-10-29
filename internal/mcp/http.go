package mcp

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Oasis1i/bill-snap/internal/store"
)

const maxBody = 1 << 20

// NewHandler is the Streamable HTTP MCP endpoint mounted at /mcp.
// An empty token disables it: every method answers 404 and no tool runs.
// The handler is stateless. It does not issue Mcp-Session-Id. A client that
// sends one is still served, because this process never opened a session.
// GET is 405. Responses are a single JSON object unless the client accepts
// only text/event-stream, in which case the same object is one SSE event.
// uploadDir is the only directory get_bill_image will read.
func NewHandler(st *store.Store, loc *time.Location, token, uploadDir string) http.Handler {
	token = strings.TrimSpace(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveHTTP(w, r, st, loc, token, uploadDir)
	})
}

type httpErrorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func serveHTTP(w http.ResponseWriter, r *http.Request, st *store.Store, loc *time.Location, token, uploadDir string) {
	if token == "" {
		writeHTTPError(w, http.StatusNotFound, "not_found", "接口未启用")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeHTTPError(w, http.StatusMethodNotAllowed, "method_not_allowed", "只支持POST")
		return
	}
	if !authorized(r, token) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="bill-snap-mcp"`)
		writeHTTPError(w, http.StatusUnauthorized, "unauthorized", "未授权")
		return
	}
	if v := strings.TrimSpace(r.Header.Get("MCP-Protocol-Version")); v != "" && !supportedProtocol(v) {
		writeRPC(w, http.StatusBadRequest, rpcFail(json.RawMessage("null"), codeBadReq, "协议版本不支持"), false)
		return
	}
	jsonOK, sseOnly := acceptMode(r.Header.Get("Accept"))
	if !jsonOK && !sseOnly {
		writeHTTPError(w, http.StatusNotAcceptable, "not_acceptable", "不接受该响应类型")
		return
	}
	if !jsonContent(r.Header.Get("Content-Type")) {
		writeHTTPError(w, http.StatusUnsupportedMediaType, "unsupported_media", "内容类型不支持")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		writeRPC(w, http.StatusBadRequest, rpcFail(json.RawMessage("null"), codeParse, "请求无法解析"), false)
		return
	}
	if len(body) > maxBody {
		writeRPC(w, http.StatusBadRequest, rpcFail(json.RawMessage("null"), codeBadReq, "请求过大"), false)
		return
	}
	payload, status, notification := handleMessage(r.Context(), st, loc, uploadDir, bytes.TrimSpace(body))
	if notification {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeRPC(w, status, payload, sseOnly)
}

func authorized(r *http.Request, token string) bool {
	got := bearer(r.Header.Get("Authorization"))
	if got == "" || token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

func bearer(header string) string {
	h := strings.TrimSpace(header)
	const prefix = "bearer "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

func acceptMode(header string) (jsonOK, sseOnly bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return true, false
	}
	sse := false
	for _, part := range strings.Split(header, ",") {
		media, _, _ := strings.Cut(part, ";")
		media = strings.TrimSpace(strings.ToLower(media))
		switch media {
		case "application/json", "application/*", "*/*":
			jsonOK = true
		case "text/event-stream":
			sse = true
		}
	}
	return jsonOK, sse && !jsonOK
}

func jsonContent(header string) bool {
	header = strings.TrimSpace(header)
	if header == "" {
		return false
	}
	media, _, _ := strings.Cut(header, ";")
	media = strings.TrimSpace(strings.ToLower(media))
	return media == "application/json" || strings.HasSuffix(media, "+json")
}

func writeHTTPError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(httpErrorBody{Error: code, Message: message})
}

func writeRPC(w http.ResponseWriter, status int, body []byte, sse bool) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	body = bytes.TrimSpace(body)
	if sse {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte("event: message\ndata: "))
		_, _ = w.Write(body)
		_, _ = w.Write([]byte("\n\n"))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
	_, _ = w.Write([]byte("\n"))
}
