package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Oasis1i/bill-snap/internal/model"
	"github.com/Oasis1i/bill-snap/internal/store"
)

const (
	readTokenForTest  = "READ_TOKEN_SHOULD_NOT_LEAK"
	writeTokenForTest = "WRITE_TOKEN_SHOULD_NOT_LEAK"
)

func TestAddBillValidation(t *testing.T) {
	loc := shanghaiLoc(t)
	now := time.Date(2026, 10, 29, 8, 0, 0, 0, loc)
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"missing amount", `{"category":"餐饮"}`, msgAmountFormat},
		{"bad amount", `{"amount":"abc","category":"餐饮"}`, msgAmountFormat},
		{"bool amount", `{"amount":true,"category":"餐饮"}`, msgAmountFormat},
		{"zero", `{"amount":0,"category":"餐饮"}`, msgAmountPositive},
		{"negative", `{"amount":"-1.20","category":"餐饮"}`, msgAmountPositive},
		{"zero decimals", `{"amount":"0.00","category":"餐饮"}`, msgAmountPositive},
		{"three decimals", `{"amount":"1.234","category":"餐饮"}`, msgAmountDecimals},
		{"trailing zeros over two", `{"amount":"1.230","category":"餐饮"}`, msgAmountDecimals},
		{"over limit", `{"amount":"1000000.01","category":"餐饮"}`, msgAmountLimit},
		{"way over", `{"amount":1000001,"category":"餐饮"}`, msgAmountLimit},
		{"missing category", `{"amount":"1.00"}`, categoryMessage()},
		{"unknown category", `{"amount":"1.00","category":"外卖配送"}`, categoryMessage()},
		{"blank category", `{"amount":"1.00","category":"  "}`, categoryMessage()},
		{"bad time", `{"amount":"1","category":"餐饮","paid_at":"昨天"}`, msgTimeFormat},
		{"time with seconds", `{"amount":"1","category":"餐饮","paid_at":"2026-10-29 08:00:00"}`, msgTimeFormat},
		{"too late", `{"amount":"1","category":"餐饮","paid_at":"2026-10-30 09:00"}`, msgTimeRange},
		{"too early", `{"amount":"1","category":"餐饮","paid_at":"1999-12-31"}`, msgTimeRange},
		{"early instant", `{"amount":"1","category":"餐饮","paid_at":"1999-12-31T15:59:00Z"}`, msgTimeRange},
		{"merchant long", `{"amount":"1","category":"餐饮","merchant":"` + strings.Repeat("店", 51) + `"}`, msgMerchantLen},
		{"note long", `{"amount":"1","category":"餐饮","note":"` + strings.Repeat("注", 201) + `"}`, msgNoteLen},
		{"request id long", `{"amount":"1","category":"餐饮","request_id":"` + strings.Repeat("k", 65) + `"}`, msgRequestIDLen},
		{"unknown field", `{"amount":"1","category":"餐饮","foo":1}`, "不认识的参数：foo"},
		{"two unknown", `{"amount":"1","category":"餐饮","b":1,"a":2}`, "不认识的参数：a、b"},
		{"bad merchant type", `{"amount":"1","category":"餐饮","merchant":1}`, msgBadArgs},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, msg := parseAddBill(json.RawMessage(tc.raw), loc, now)
			if msg != tc.want {
				t.Fatalf("got %q want %q", msg, tc.want)
			}
			assertPhrase(t, msg)
		})
	}

	okCases := []struct {
		raw   string
		cents int64
	}{
		{`{"amount":"25.9","category":"餐饮"}`, 2590},
		{`{"amount":25.9,"category":"餐饮"}`, 2590},
		{`{"amount":"8000","category":"工资"}`, 800000},
		{`{"amount":8000,"category":"工资"}`, 800000},
		{`{"amount":"1000000","category":"其他"}`, 100000000},
		{`{"amount":"1000000.00","category":"其他"}`, 100000000},
		{`{"amount":"0.01","category":"餐饮"}`, 1},
		{`{"amount":" 12.50 ","category":" 外卖 "}`, 1250},
	}
	for _, tc := range okCases {
		got, msg := parseAddBill(json.RawMessage(tc.raw), loc, now)
		if msg != "" || got.AmountCents != tc.cents {
			t.Fatalf("%s -> %+v %q", tc.raw, got, msg)
		}
	}

	paid, msg := parseAddBill(json.RawMessage(`{"amount":"1","category":"餐饮","paid_at":"2026-10-29"}`), loc, now)
	if msg != "" {
		t.Fatal(msg)
	}
	wantNoon := time.Date(2026, 10, 29, 12, 0, 0, 0, loc)
	if !paid.PaidAt.Equal(wantNoon) {
		t.Fatal(paid.PaidAt)
	}
	paid, msg = parseAddBill(json.RawMessage(`{"amount":"1","category":"餐饮","paid_at":"2026-10-29 08:30"}`), loc, now)
	if msg != "" || !paid.PaidAt.Equal(time.Date(2026, 10, 29, 8, 30, 0, 0, loc)) {
		t.Fatalf("%+v %s", paid.PaidAt, msg)
	}
	edge, msg := parseAddBill(json.RawMessage(`{"amount":"1","category":"餐饮","paid_at":"1999-12-31T16:00:00Z"}`), loc, now)
	if msg != "" || !edge.PaidAt.Equal(time.Date(2000, 1, 1, 0, 0, 0, 0, loc)) {
		t.Fatalf("%+v %s", edge.PaidAt, msg)
	}
	latest, msg := parseAddBill(json.RawMessage(`{"amount":"1","category":"餐饮","paid_at":"2026-10-30 08:00"}`), loc, now)
	if msg != "" || !latest.PaidAt.Equal(time.Date(2026, 10, 30, 8, 0, 0, 0, loc)) {
		t.Fatalf("%+v %s", latest.PaidAt, msg)
	}
	missing, msg := parseAddBill(json.RawMessage(`{"amount":"1","category":"餐饮"}`), loc, now)
	if msg != "" || !missing.PaidAt.Equal(now) {
		t.Fatalf("%+v %s", missing.PaidAt, msg)
	}

	longItems := strings.Repeat("面", 120)
	got, msg := parseAddBill(json.RawMessage(`{"amount":"1","category":"餐饮","items":"`+longItems+`"}`), loc, now)
	if msg != "" || utf8.RuneCountInString(got.Items) != model.MaxItemRunes {
		t.Fatalf("items %d %q %s", utf8.RuneCountInString(got.Items), got.Items, msg)
	}
}

func TestAddBillAuthIdempotencyAuditAndWarning(t *testing.T) {
	st, loc := testStore(t)
	logs := captureMCPLog(t)
	fixed := time.Date(2026, 10, 29, 8, 0, 0, 0, loc)
	prev := clock
	clock = func() time.Time { return fixed }
	t.Cleanup(func() { clock = prev })

	readOnly := NewHandler(st, loc, readTokenForTest, "", "")
	names := toolNames(t, readOnly, readTokenForTest)
	if _, ok := names["add_bill"]; ok || len(names) != 4 {
		t.Fatalf("read tools %v", names)
	}
	text, isErr := callAddBill(t, readOnly, readTokenForTest, `{"amount":"1","category":"餐饮"}`)
	if !isErr || text != msgWriteDisabled {
		t.Fatalf("disabled %v %q", isErr, text)
	}
	assertNoToken(t, logs.String())
	audits := mustAudits(t, st)
	if len(audits) != 1 || audits[0].TokenKind != tokenKindRead || audits[0].Tool != "add_bill" || audits[0].Result != "error:"+msgWriteDisabled || audits[0].BillID.Valid {
		t.Fatalf("%+v", audits)
	}
	if strings.Contains(audits[0].ArgsJSON, readTokenForTest) {
		t.Fatal(audits[0].ArgsJSON)
	}

	h := NewHandler(st, loc, readTokenForTest, writeTokenForTest, "")
	names = toolNames(t, h, readTokenForTest)
	if _, ok := names["add_bill"]; ok || len(names) != 4 {
		t.Fatalf("read still hides add_bill %v", names)
	}
	text, isErr = callAddBill(t, h, readTokenForTest, `{"amount":"12","category":"日用","note":"现金买菜"}`)
	if !isErr || text != msgNoWritePerm {
		t.Fatalf("read call %v %q", isErr, text)
	}
	names = toolNames(t, h, writeTokenForTest)
	if _, ok := names["add_bill"]; !ok || len(names) != 5 {
		t.Fatalf("write tools %v", names)
	}
	if !strings.Contains(names["add_bill"], "需要写权限") {
		t.Fatal(names["add_bill"])
	}

	rec := postMCP(h, "nope", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "add_bill", "arguments": map[string]any{"amount": "1", "category": "餐饮"}},
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token %d %s", rec.Code, rec.Body.String())
	}
	if n := len(mustAudits(t, st)); n != 2 {
		t.Fatalf("401 should not audit, audits %d", n)
	}

	insertBill(t, st, store.NewBill{
		AmountCents: 3200,
		Currency:    "CNY",
		Merchant:    "菜市场",
		Category:    "日用",
		PaidAt:      time.Date(2026, 10, 29, 1, 0, 0, 0, time.UTC),
		Note:        "截图",
		ImagePath:   "data/uploads/veg.png",
		ContentHash: strings.Repeat("a", 64),
	})

	body, isErr := callAddBill(t, h, writeTokenForTest, `{"amount":"32","category":"日用","merchant":"菜市场","items":"青菜 ×2","note":"现金买菜","paid_at":"2026-10-29 15:00"}`)
	if isErr {
		t.Fatal(body)
	}
	first := decodeAdd(t, body)
	if !first.Created || first.Bill.Amount != "32.00" || first.Bill.Category != "日用" || first.Bill.Source != "manual" || first.Bill.HasImage || first.Bill.Items != "青菜 ×2" || first.Bill.Note != "现金买菜" || first.Bill.PaidAt != "2026-10-29 15:00" {
		t.Fatalf("%+v", first)
	}
	if !strings.Contains(first.Warning, "请确认不是重复") || !strings.Contains(first.Warning, "id ") {
		t.Fatalf("warning %q", first.Warning)
	}
	shot, ok, err := st.FindByContentHash(context.Background(), strings.Repeat("a", 64))
	if err != nil || !ok || shot.Note != "截图" || shot.Source != "screenshot" || shot.Items != "" {
		t.Fatalf("screenshot changed %+v", shot)
	}

	again, isErr := callAddBill(t, h, writeTokenForTest, `{"amount":"32.00","category":"日用","paid_at":"2026-10-29 18:00","request_id":"cash-veg"}`)
	if isErr {
		t.Fatal(again)
	}
	second := decodeAdd(t, again)
	if !second.Created || second.Bill.ID == first.Bill.ID || second.Warning != "" {
		t.Fatalf("request_id should save without a warning: %+v", second)
	}

	replayRaw := `{"amount":"99","category":"工资","request_id":"cash-veg","paid_at":"2026-10-29 18:00"}`
	replay, isErr := callAddBill(t, h, writeTokenForTest, replayRaw)
	if isErr {
		t.Fatal(replay)
	}
	third := decodeAdd(t, replay)
	if third.Created || third.Bill.ID != second.Bill.ID || third.Bill.Amount != "32.00" || third.Bill.Category != "日用" || third.Warning != "" {
		t.Fatalf("idempotent %+v", third)
	}
	rows, err := st.List(context.Background(), store.ListFilter{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	manuals := 0
	for _, row := range rows {
		if row.Source != "manual" {
			continue
		}
		manuals++
		if !strings.HasPrefix(row.ContentHash, "manual:") || !strings.HasPrefix(row.DedupKey, "manual:") || row.ContentHash == row.DedupKey {
			t.Fatalf("manual keys %+v", row)
		}
	}
	if manuals != 2 {
		t.Fatalf("manual rows %d of %+v", manuals, rows)
	}

	wage, isErr := callAddBill(t, h, writeTokenForTest, `{"amount":"8000","category":"工资","paid_at":"2026-10-30 08:00","request_id":"salary-nov"}`)
	if isErr {
		t.Fatal(wage)
	}
	salary := decodeAdd(t, wage)
	if !salary.Created || salary.Warning != "" || salary.Bill.Amount != "8000.00" || salary.Bill.Category != "工资" || salary.Bill.Source != "manual" {
		t.Fatalf("%+v", salary)
	}
	sum := decodeSpend(t, callAs(t, h, writeTokenForTest, "summarize_spend", `{"from":"2026-10-30","to":"2026-10-30"}`))
	if sum.Count != 0 || sum.Amount != "0.00" {
		t.Fatalf("income included by default %+v", sum)
	}
	sum = decodeSpend(t, callAs(t, h, writeTokenForTest, "summarize_spend", `{"from":"2026-10-30","to":"2026-10-30","include_income":true}`))
	if sum.Count != 1 || sum.Amount != "8000.00" {
		t.Fatalf("include income %+v", sum)
	}

	listed := callAs(t, h, writeTokenForTest, "list_bills", `{"from":"2026-10-30","to":"2026-10-30"}`)
	if !strings.Contains(listed, `"items"`) || !strings.Contains(listed, `"source": "manual"`) || !strings.Contains(listed, `"note"`) || strings.Contains(listed, "manual:") {
		t.Fatalf("list %s", listed)
	}

	def, isErr := callAddBill(t, h, writeTokenForTest, `{"amount":"1.5","category":"其他"}`)
	if isErr {
		t.Fatal(def)
	}
	defaulted := decodeAdd(t, def)
	if defaulted.Bill.PaidAt != "2026-10-29 08:00" || defaulted.Warning != "" {
		t.Fatalf("default now %+v", defaulted)
	}

	bad, isErr := callAddBill(t, h, writeTokenForTest, `{"amount":"-5","category":"餐饮"}`)
	if !isErr || bad != msgAmountPositive {
		t.Fatalf("http validation %v %q", isErr, bad)
	}

	audits = mustAudits(t, st)
	sawReadDeny := false
	sawWriteOK := false
	sawWriteErr := false
	for _, row := range audits {
		if strings.Contains(row.ArgsJSON, writeTokenForTest) || strings.Contains(row.ArgsJSON, readTokenForTest) {
			t.Fatalf("token stored in audit %s", row.ArgsJSON)
		}
		if row.TokenKind != tokenKindRead && row.TokenKind != tokenKindWrite {
			t.Fatalf("kind %s", row.TokenKind)
		}
		if row.Result == "error:"+msgNoWritePerm && row.TokenKind == tokenKindRead && !row.BillID.Valid {
			sawReadDeny = true
		}
		if row.Result == "ok" && row.TokenKind == tokenKindWrite && row.BillID.Valid {
			sawWriteOK = true
		}
		if row.Result == "error:"+msgAmountPositive && row.TokenKind == tokenKindWrite {
			sawWriteErr = true
		}
	}
	if !sawReadDeny || !sawWriteOK || !sawWriteErr {
		t.Fatalf("audit coverage %+v", audits)
	}
	assertNoToken(t, logs.String())
	if !strings.Contains(logs.String(), "token_kind=write") || !strings.Contains(logs.String(), "token_kind=read") || !strings.Contains(logs.String(), `msg="mcp audit"`) {
		t.Fatal(logs.String())
	}
}

func TestStdioHidesAddBill(t *testing.T) {
	st, loc := testStore(t)
	var in, out bytes.Buffer
	for _, m := range []any{
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{
			"name": "add_bill", "arguments": map[string]any{"amount": "1", "category": "餐饮"},
		}},
	} {
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		in.Write(b)
		in.WriteByte('\n')
	}
	if err := Serve(&in, &out, st, loc, ""); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatal(out.String())
	}
	var listed map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &listed); err != nil {
		t.Fatal(err)
	}
	tools := listed["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 4 {
		t.Fatal(tools)
	}
	var called map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &called); err != nil {
		t.Fatal(err)
	}
	result := called["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatal(called)
	}
	if result["content"].([]any)[0].(map[string]any)["text"] != msgWriteDisabled {
		t.Fatal(result)
	}
}

func shanghaiLoc(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func captureMCPLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func toolNames(t *testing.T, h http.Handler, token string) map[string]string {
	t.Helper()
	rec := postMCP(h, token, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if rec.Code != http.StatusOK {
		t.Fatalf("list %d %s", rec.Code, rec.Body.String())
	}
	msg := decodeRPC(t, rec.Body.String())
	out := map[string]string{}
	for _, tool := range msg["result"].(map[string]any)["tools"].([]any) {
		item := tool.(map[string]any)
		out[item["name"].(string)] = item["description"].(string)
	}
	return out
}

func callAddBill(t *testing.T, h http.Handler, token, args string) (string, bool) {
	t.Helper()
	return callAsResult(t, h, token, "add_bill", args)
}

func callAs(t *testing.T, h http.Handler, token, name, args string) string {
	t.Helper()
	text, isErr := callAsResult(t, h, token, name, args)
	if isErr {
		t.Fatalf("%s error %s", name, text)
	}
	return text
}

func callAsResult(t *testing.T, h http.Handler, token, name, args string) (string, bool) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), token) {
		t.Fatalf("token leaked in response %s", rec.Body.String())
	}
	msg := decodeRPC(t, rec.Body.String())
	result := msg["result"].(map[string]any)
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	isErr, _ := result["isError"].(bool)
	return text, isErr
}

type addPayload struct {
	Created bool   `json:"created"`
	Warning string `json:"warning"`
	Bill    struct {
		ID       int64  `json:"id"`
		Amount   string `json:"amount"`
		Category string `json:"category"`
		Merchant string `json:"merchant"`
		PaidAt   string `json:"paid_at"`
		Items    string `json:"items"`
		Note     string `json:"note"`
		Source   string `json:"source"`
		HasImage bool   `json:"has_image"`
	} `json:"bill"`
}

func decodeAdd(t *testing.T, text string) addPayload {
	t.Helper()
	var out addPayload
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func mustAudits(t *testing.T, st *store.Store) []store.AuditRow {
	t.Helper()
	rows, err := st.ListAudits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func assertNoToken(t *testing.T, text string) {
	t.Helper()
	if strings.Contains(text, readTokenForTest) || strings.Contains(text, writeTokenForTest) {
		t.Fatalf("token leaked: %s", text)
	}
}
