package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Oasis1i/bill-snap/internal/model"
	"github.com/Oasis1i/bill-snap/internal/store"
)

func TestHTTPAuthAndMethods(t *testing.T) {
	st, loc := testStore(t)
	disabled := NewHandler(st, loc, "", "", "")
	rec := postMCP(disabled, "", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled %d %s", rec.Code, rec.Body.String())
	}
	assertPhrase(t, httpMessage(t, rec.Body.String()))

	h := NewHandler(st, loc, "mcp-secret", "", "")
	rec = postMCP(h, "", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-06-18"}})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing %d %s", rec.Code, rec.Body.String())
	}
	rec = postMCP(h, "upload-secret", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong %d %s", rec.Code, rec.Body.String())
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Token", "mcp-secret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("x-api-token %d %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer mcp-secret")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" {
		t.Fatalf("get %d allow %q %s", rec.Code, rec.Header().Get("Allow"), rec.Body.String())
	}

	for _, ver := range []string{"2025-06-18", "2025-03-26", "2024-11-05"} {
		rec = postMCP(h, "mcp-secret", map[string]any{
			"jsonrpc": "2.0", "id": "init", "method": "initialize",
			"params": map[string]any{"protocolVersion": ver, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "0"}},
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %d %s", ver, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Mcp-Session-Id") != "" {
			t.Fatal("session id issued")
		}
		msg := decodeRPC(t, rec.Body.String())
		if msg["result"].(map[string]any)["protocolVersion"] != ver {
			t.Fatalf("%s -> %v", ver, msg)
		}
		if msg["id"] != "init" {
			t.Fatalf("id %v", msg["id"])
		}
	}

	req = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	req.Header.Set("Authorization", "Bearer mcp-secret")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", "client-made")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted || strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("notify %d %q", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer mcp-secret")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", "1999-01-01")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "协议版本不支持") {
		t.Fatalf("version %d %s", rec.Code, rec.Body.String())
	}
}

func TestHTTPToolsListSchemaAndCall(t *testing.T) {
	st, loc := testStore(t)
	insertBill(t, st, store.NewBill{
		AmountCents:  2590,
		Currency:     "CNY",
		Merchant:     "美团外卖",
		Category:     "外卖",
		PaidAt:       time.Date(2026, 10, 26, 12, 30, 0, 0, time.UTC),
		Note:         "午饭",
		ImagePath:    "data/uploads/secret.png",
		ContentHash:  strings.Repeat("a", 64),
		RawModelJSON: `{"model":"secret-model"}`,
	})
	insertBill(t, st, store.NewBill{
		AmountCents:  10000,
		Currency:     "CNY",
		Merchant:     "公司",
		Category:     "工资",
		PaidAt:       time.Date(2026, 10, 27, 1, 0, 0, 0, time.UTC),
		Note:         model.PaidAtDefaultedNote,
		ImagePath:    "data/uploads/wage.png",
		ContentHash:  strings.Repeat("b", 64),
		RawModelJSON: `{"model":"secret-model"}`,
	})
	h := NewHandler(st, loc, "mcp-secret", "", "")

	rec := postMCP(h, "mcp-secret", map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	msg := decodeRPC(t, rec.Body.String())
	tools := msg["result"].(map[string]any)["tools"].([]any)
	byName := map[string]map[string]any{}
	for _, tool := range tools {
		item := tool.(map[string]any)
		byName[item["name"].(string)] = item
	}
	if byName["list_bills"]["description"] != "按日期范围列出账单明细，金额单位为元，时间按 Asia/Shanghai" {
		t.Fatal(byName["list_bills"])
	}
	if byName["summarize_spend"]["description"] != "按日期范围汇总支出，给出总额和各类别金额与笔数" {
		t.Fatal(byName["summarize_spend"])
	}
	listSchema := byName["list_bills"]["inputSchema"].(map[string]any)
	if listSchema["type"] != "object" {
		t.Fatal(listSchema)
	}
	reqd := listSchema["required"].([]any)
	if len(reqd) != 2 || reqd[0] != "from" || reqd[1] != "to" {
		t.Fatal(reqd)
	}
	props := listSchema["properties"].(map[string]any)
	enum := props["category"].(map[string]any)["enum"].([]any)
	cats := model.Categories()
	if len(enum) != len(cats) {
		t.Fatal(enum)
	}
	for i, c := range cats {
		if enum[i] != c {
			t.Fatalf("enum %v", enum)
		}
	}
	limit := props["limit"].(map[string]any)
	if limit["default"].(float64) != 50 || limit["maximum"].(float64) != 200 {
		t.Fatal(limit)
	}
	sumProps := byName["summarize_spend"]["inputSchema"].(map[string]any)["properties"].(map[string]any)
	groupEnum := sumProps["group_by"].(map[string]any)["enum"].([]any)
	if len(groupEnum) != 2 || groupEnum[0] != "category" || groupEnum[1] != "day" {
		t.Fatal(groupEnum)
	}
	if sumProps["include_income"].(map[string]any)["type"] != "boolean" {
		t.Fatal(sumProps["include_income"])
	}
	catSchema := byName["list_categories"]["inputSchema"].(map[string]any)
	if catSchema["type"] != "object" || len(catSchema["properties"].(map[string]any)) != 0 {
		t.Fatal(catSchema)
	}

	body := callToolHTTP(t, h, "list_bills", map[string]any{"from": "2026-10-01", "to": "2026-10-31", "category": "外卖"})
	var listed struct {
		Count int `json:"count"`
		Bills []struct {
			ID       int64  `json:"id"`
			PaidAt   string `json:"paid_at"`
			Amount   string `json:"amount"`
			Category string `json:"category"`
			Merchant string `json:"merchant"`
			Note     string `json:"note"`
		} `json:"bills"`
	}
	if err := json.Unmarshal([]byte(body), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Count != 1 || listed.Bills[0].Amount != "25.90" || listed.Bills[0].PaidAt != "2026-10-26 20:30" || listed.Bills[0].Merchant != "美团外卖" || listed.Bills[0].Note != "午饭" || listed.Bills[0].Category != "外卖" {
		t.Fatalf("%s", body)
	}
	if !strings.Contains(body, `"has_image": false`) {
		t.Fatalf("has_image %s", body)
	}
	assertNoSecrets(t, body)

	sum := decodeSpend(t, callToolHTTP(t, h, "summarize_spend", map[string]any{"from": "2026-10-01", "to": "2026-10-31"}))
	if sum.Amount != "25.90" || sum.Count != 1 || len(sum.Groups) != 1 || sum.Groups[0].Category != "外卖" || sum.Groups[0].Count != 1 {
		t.Fatalf("%+v", sum)
	}
	sum = decodeSpend(t, callToolHTTP(t, h, "summarize_spend", map[string]any{"from": "2026-10-01", "to": "2026-10-31", "include_income": true, "group_by": "day"}))
	if sum.Amount != "125.90" || sum.Count != 2 || sum.GroupBy != "day" || len(sum.Groups) != 2 {
		t.Fatalf("%+v", sum)
	}
	if sum.Groups[0].Day != "2026-10-26" || sum.Groups[0].Amount != "25.90" || sum.Groups[1].Day != "2026-10-27" || sum.Groups[1].Amount != "100.00" {
		t.Fatalf("%+v", sum)
	}

	wage := callToolHTTP(t, h, "list_bills", map[string]any{"from": "2026-10-27", "to": "2026-10-27", "category": "工资"})
	if !strings.Contains(wage, "未识别到支付时间") || strings.Contains(wage, "paid_at missing") || strings.Contains(wage, "wage.png") {
		t.Fatalf("%s", wage)
	}

	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"from": "2026-1-1", "to": "2026-10-31"}, msgBadDate},
		{map[string]any{"from": "2026-10-31", "to": "2026-10-01"}, msgRangeOrder},
		{map[string]any{"from": "2024-01-01", "to": "2025-01-01"}, msgSpan},
		{map[string]any{"from": "2026-10-01", "to": "2026-10-31", "category": "没有"}, ""},
		{map[string]any{"from": "2026-10-01", "to": "2026-10-31", "limit": 0}, msgBadLimit},
	}
	for _, tc := range cases {
		text, isErr := callToolResult(t, h, "list_bills", tc.args)
		if !isErr {
			t.Fatalf("expected error for %v: %s", tc.args, text)
		}
		assertPhrase(t, text)
		if tc.want != "" && text != tc.want {
			t.Fatalf("got %q want %q", text, tc.want)
		}
		if tc.args["category"] == "没有" && (!strings.HasPrefix(text, "未知分类 允许") || !strings.Contains(text, "外卖") || !strings.Contains(text, "其他")) {
			t.Fatal(text)
		}
	}

	text, isErr := callToolResult(t, h, "list_bills", map[string]any{"from": "2026-10-01", "to": "2026-10-31", "limit": 500})
	if isErr || !strings.Contains(text, "25.90") {
		t.Fatalf("clamp %v %s", isErr, text)
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer mcp-secret")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(rec.Body.String(), "event: message") || !strings.Contains(rec.Body.String(), "list_bills") {
		t.Fatalf("sse %d %s %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}

func testStore(t *testing.T) (*store.Store, *time.Location) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "bills.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	st.SetLocation(loc)
	return st, loc
}

func insertBill(t *testing.T, st *store.Store, in store.NewBill) {
	t.Helper()
	if _, _, err := st.InsertBill(context.Background(), in); err != nil {
		t.Fatal(err)
	}
}

func postMCP(h http.Handler, token string, body any) *httptest.ResponseRecorder {
	b, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeRPC(t *testing.T, raw string) map[string]any {
	t.Helper()
	var msg map[string]any
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	return msg
}

func callToolHTTP(t *testing.T, h http.Handler, name string, args map[string]any) string {
	t.Helper()
	text, isErr := callToolResult(t, h, name, args)
	if isErr {
		t.Fatalf("tool error %s", text)
	}
	assertNoSecrets(t, text)
	return text
}

func callToolResult(t *testing.T, h http.Handler, name string, args map[string]any) (string, bool) {
	t.Helper()
	rec := postMCP(h, "mcp-secret", map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": args},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	msg := decodeRPC(t, rec.Body.String())
	if msg["error"] != nil {
		t.Fatalf("rpc error %v", msg["error"])
	}
	result := msg["result"].(map[string]any)
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	isErr, _ := result["isError"].(bool)
	return text, isErr
}

type spendPayload struct {
	Amount  string `json:"amount"`
	Count   int    `json:"count"`
	GroupBy string `json:"group_by"`
	Groups  []struct {
		Category string `json:"category"`
		Day      string `json:"day"`
		Amount   string `json:"amount"`
		Count    int    `json:"count"`
	} `json:"groups"`
}

func decodeSpend(t *testing.T, text string) spendPayload {
	t.Helper()
	var sum spendPayload
	if err := json.Unmarshal([]byte(text), &sum); err != nil {
		t.Fatal(err)
	}
	return sum
}

func assertNoSecrets(t *testing.T, text string) {
	t.Helper()
	for _, secret := range []string{"image_path", "raw_model_json", "secret.png", "wage.png", "secret-model", "amount_cents", "content_hash"} {
		if strings.Contains(text, secret) {
			t.Fatalf("exposed %s in %s", secret, text)
		}
	}
}

func httpMessage(t *testing.T, raw string) string {
	t.Helper()
	var body struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	return body.Message
}

func assertPhrase(t *testing.T, text string) {
	t.Helper()
	if strings.Contains(text, "，") || strings.Contains(text, ",") || strings.Contains(text, "。") || strings.HasSuffix(strings.TrimSpace(text), ".") {
		t.Fatalf("phrase %q", text)
	}
}

func TestHTTPRejectsBadAccept(t *testing.T) {
	st, loc := testStore(t)
	h := NewHandler(st, loc, "mcp-secret", "", "")
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Authorization", "Bearer mcp-secret")
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotAcceptable {
		t.Fatalf("accept %d %s", rec.Code, rec.Body.String())
	}
	req.Header.Set("Accept", "application/json")
	req.Body = io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("type %d %s", rec.Code, rec.Body.String())
	}
}
