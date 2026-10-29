package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Oasis1i/bill-snap/internal/model"
	"github.com/Oasis1i/bill-snap/internal/store"
)

const (
	tokenKindRead  = "read"
	tokenKindWrite = "write"
	tokenKindStdio = "stdio"

	maxYuan           int64 = 1000000
	maxMerchantRunes        = 50
	maxNoteRunes            = 200
	maxRequestIDRunes       = 64

	msgNoWritePerm    = "没有写入权限"
	msgWriteDisabled  = "写入功能未开启"
	msgAmountFormat   = "金额格式不对"
	msgAmountPositive = "金额必须大于0"
	msgAmountDecimals = "金额最多两位小数"
	msgAmountLimit    = "金额超过上限"
	msgTimeFormat     = "时间格式应为 2026-10-30 或 2026-10-30 12:30"
	msgTimeRange      = "时间超出允许范围"
	msgMerchantLen    = "商家最多50字"
	msgNoteLen        = "备注最多200字"
	msgRequestIDLen   = "request_id 过长"
	msgSaveFailed     = "保存失败，请稍后再试"
)

// clock is the source of "now" for a missing paid_at. Tests replace it.
var clock = time.Now

// Access is the caller of one MCP message. Kind is stored in the audit log.
// The bearer token itself is never copied onto this value.
type Access struct {
	Kind            string
	WriteConfigured bool
}

// CanWrite is true only for the write-token bearer while MCP_WRITE_TOKEN is set.
func (a Access) CanWrite() bool {
	return a.WriteConfigured && a.Kind == tokenKindWrite
}

func addBillTool() map[string]any {
	return map[string]any{
		"name":        "add_bill",
		"description": "手动记一笔账（现金、无截图消费、每月工资/房租）。调用前必须先向用户复述并得到确认。需要写权限 token。",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"amount": map[string]any{
					"type":        []any{"string", "number"},
					"description": "金额 单位元 大于0 最多两位小数 上限1000000",
				},
				"category": map[string]any{
					"type":        "string",
					"enum":        model.Categories(),
					"description": "必须是配置的分类之一",
				},
				"merchant": map[string]any{
					"type":        "string",
					"description": "商家 可选 最多50字",
				},
				"paid_at": map[string]any{
					"type":        "string",
					"description": "可选 RFC3339 或 YYYY-MM-DD 或 YYYY-MM-DD HH:MM 按北京时间 默认现在",
				},
				"items": map[string]any{
					"type":        "string",
					"description": "商品名 可选 多个用、分隔 最多100字",
				},
				"note": map[string]any{
					"type":        "string",
					"description": "备注 可选 最多200字",
				},
				"request_id": map[string]any{
					"type":        "string",
					"description": "可选 幂等键 同一键重复调用返回同一笔 最多64字符",
				},
			},
			"required":             []any{"amount", "category"},
			"additionalProperties": false,
		},
	}
}

type addBillView struct {
	ID       int64  `json:"id"`
	Amount   string `json:"amount"`
	Category string `json:"category"`
	Merchant string `json:"merchant"`
	PaidAt   string `json:"paid_at"`
	Items    string `json:"items"`
	Note     string `json:"note"`
	Source   string `json:"source"`
	HasImage bool   `json:"has_image"`
}

type addBillPayload struct {
	Created bool        `json:"created"`
	Bill    addBillView `json:"bill"`
	Warning string      `json:"warning,omitempty"`
}

func addBill(ctx context.Context, st *store.Store, loc *time.Location, uploadDir string, access Access, raw json.RawMessage, now time.Time) toolResult {
	argsJSON := string(bytes.TrimSpace(raw))
	if argsJSON == "" || argsJSON == "null" {
		argsJSON = "{}"
	}
	fail := func(msg string) toolResult {
		return auditFailure(ctx, st, access, argsJSON, msg)
	}
	if !access.WriteConfigured {
		return fail(msgWriteDisabled)
	}
	if access.Kind != tokenKindWrite {
		return fail(msgNoWritePerm)
	}
	in, msg := parseAddBill(raw, loc, now)
	if msg != "" {
		return fail(msg)
	}
	warn := ""
	if strings.TrimSpace(in.RequestID) == "" {
		if st == nil {
			slog.Error("mcp add_bill", "err", "nil store")
			return fail(msgSaveFailed)
		}
		id, found, err := st.FindSameDayAmountCategory(ctx, in.AmountCents, in.Category, in.PaidAt)
		if err != nil {
			slog.Error("mcp add_bill lookup", "err", err)
			return fail(msgSaveFailed)
		}
		if found {
			warn = fmt.Sprintf("同一天已有一笔相同金额和类别的账（id %d），请确认不是重复", id)
		}
	}
	if st == nil {
		slog.Error("mcp add_bill", "err", "nil store")
		return fail(msgSaveFailed)
	}
	bill, created, err := st.InsertManualBill(ctx, in, access.Kind, "add_bill", argsJSON)
	if err != nil {
		slog.Error("mcp add_bill", "err", err)
		return fail(msgSaveFailed)
	}
	if !created {
		warn = ""
	}
	logAudit(access.Kind, "ok", &bill.ID)
	view := addBillView{
		ID:       bill.ID,
		Amount:   yuan(bill),
		Category: bill.Category,
		Merchant: bill.Merchant,
		PaidAt:   formatPaidAt(bill.PaidAt, loc),
		Items:    bill.Items,
		Note:     bill.Note,
		Source:   bill.Source,
		HasImage: hasBillImage(uploadDir, bill.ImagePath),
	}
	return toolJSON(addBillPayload{Created: created, Bill: view, Warning: warn})
}

func auditFailure(ctx context.Context, st *store.Store, access Access, argsJSON, msg string) toolResult {
	result := "error:" + msg
	if st != nil {
		if err := st.WriteAudit(ctx, store.AuditWrite{
			TokenKind: access.Kind,
			Tool:      "add_bill",
			ArgsJSON:  argsJSON,
			Result:    result,
		}); err != nil {
			slog.Error("mcp audit write", "err", err)
		}
	} else {
		slog.Error("mcp audit write", "err", "nil store")
	}
	logAudit(access.Kind, result, nil)
	return toolError(msg)
}

// logAudit writes one structured line. systemd journald records the process
// stderr, which is where this logger goes. The bearer token is not an argument.
func logAudit(kind, result string, billID *int64) {
	args := []any{"tool", "add_bill", "token_kind", kind, "result", result}
	if billID != nil {
		args = append(args, "bill_id", *billID)
	}
	slog.Info("mcp audit", args...)
}

func parseAddBill(raw json.RawMessage, loc *time.Location, now time.Time) (store.ManualBill, string) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte(`{}`)
	}
	if raw[0] != '{' {
		return store.ManualBill{}, msgBadArgs
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return store.ManualBill{}, msgBadArgs
	}
	if msg := unknownFields(fields); msg != "" {
		return store.ManualBill{}, msg
	}
	amountRaw, ok := fields["amount"]
	if !ok {
		return store.ManualBill{}, msgAmountFormat
	}
	cents, msg := parseYuan(amountRaw)
	if msg != "" {
		return store.ManualBill{}, msg
	}
	catRaw, ok := fields["category"]
	if !ok {
		return store.ManualBill{}, categoryMessage()
	}
	category, msg := parseOptionalString(catRaw)
	if msg != "" || !model.ValidCategory(category) {
		return store.ManualBill{}, categoryMessage()
	}
	merchant, msg := parseOptionalString(fields["merchant"])
	if msg != "" {
		return store.ManualBill{}, msgBadArgs
	}
	if utf8.RuneCountInString(merchant) > maxMerchantRunes {
		return store.ManualBill{}, msgMerchantLen
	}
	paid, msg := parsePaidField(fields["paid_at"], loc, now)
	if msg != "" {
		return store.ManualBill{}, msg
	}
	items, msg := parseOptionalString(fields["items"])
	if msg != "" {
		return store.ManualBill{}, msgBadArgs
	}
	note, msg := parseOptionalString(fields["note"])
	if msg != "" {
		return store.ManualBill{}, msgBadArgs
	}
	if utf8.RuneCountInString(note) > maxNoteRunes {
		return store.ManualBill{}, msgNoteLen
	}
	requestID, msg := parseOptionalString(fields["request_id"])
	if msg != "" {
		return store.ManualBill{}, msgBadArgs
	}
	if utf8.RuneCountInString(requestID) > maxRequestIDRunes {
		return store.ManualBill{}, msgRequestIDLen
	}
	return store.ManualBill{
		AmountCents: cents,
		Merchant:    merchant,
		Category:    category,
		PaidAt:      paid,
		Items:       model.NormalizeItems(items),
		Note:        note,
		RequestID:   requestID,
	}, ""
}

func unknownFields(fields map[string]json.RawMessage) string {
	allowed := map[string]bool{
		"amount": true, "category": true, "merchant": true, "paid_at": true,
		"items": true, "note": true, "request_id": true,
	}
	var unknown []string
	for key := range fields {
		if !allowed[key] {
			unknown = append(unknown, clipKey(key))
		}
	}
	if len(unknown) == 0 {
		return ""
	}
	sort.Strings(unknown)
	if len(unknown) > 8 {
		unknown = unknown[:8]
	}
	return "不认识的参数：" + strings.Join(unknown, "、")
}

func clipKey(key string) string {
	r := []rune(key)
	if len(r) > 40 {
		return string(r[:40])
	}
	return key
}

func categoryMessage() string {
	return "类别应为：" + strings.Join(model.Categories(), "、")
}

func parsePaidField(raw json.RawMessage, loc *time.Location, now time.Time) (time.Time, string) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return parsePaidAt("", loc, now)
	}
	text, msg := parseOptionalString(raw)
	if msg != "" {
		return time.Time{}, msgTimeFormat
	}
	return parsePaidAt(text, loc, now)
}

func parsePaidAt(s string, loc *time.Location, now time.Time) (time.Time, string) {
	if loc == nil {
		loc = time.UTC
	}
	s = strings.TrimSpace(s)
	var t time.Time
	if s == "" {
		t = now
	} else if parsed, ok := tryRFC3339(s); ok {
		t = parsed
	} else if len(s) == len("2006-01-02") {
		day, ok := parseCivilDate(s, loc)
		if !ok {
			return time.Time{}, msgTimeFormat
		}
		t = time.Date(day.Year(), day.Month(), day.Day(), 12, 0, 0, 0, loc)
	} else if len(s) == len("2006-01-02 15:04") && s[10] == ' ' {
		parsed, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
		if err != nil || parsed.Format("2006-01-02 15:04") != s {
			return time.Time{}, msgTimeFormat
		}
		t = parsed
	} else {
		return time.Time{}, msgTimeFormat
	}
	earliest := time.Date(2000, 1, 1, 0, 0, 0, 0, loc)
	if t.Before(earliest) || t.After(now.Add(24*time.Hour)) {
		return time.Time{}, msgTimeRange
	}
	return t, ""
}

func tryRFC3339(s string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func parseOptionalString(raw json.RawMessage) (string, string) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", ""
	}
	if raw[0] != '"' {
		return "", msgBadArgs
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", msgBadArgs
	}
	return strings.TrimSpace(s), ""
}

func parseYuan(raw json.RawMessage) (int64, string) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return 0, msgAmountFormat
	}
	var text string
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &text); err != nil {
			return 0, msgAmountFormat
		}
	} else {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var n json.Number
		if err := dec.Decode(&n); err != nil {
			return 0, msgAmountFormat
		}
		var extra json.RawMessage
		if err := dec.Decode(&extra); err != io.EOF {
			return 0, msgAmountFormat
		}
		text = n.String()
	}
	return centsFromYuan(text)
}

func centsFromYuan(text string) (int64, string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, msgAmountFormat
	}
	neg := false
	if text[0] == '+' {
		return 0, msgAmountFormat
	}
	if text[0] == '-' {
		neg = true
		text = strings.TrimSpace(text[1:])
		if text == "" {
			return 0, msgAmountFormat
		}
	}
	if strings.ContainsAny(text, "eE+") {
		return 0, msgAmountFormat
	}
	parts := strings.Split(text, ".")
	if len(parts) > 2 {
		return 0, msgAmountFormat
	}
	whole := parts[0]
	frac := ""
	if len(parts) == 2 {
		frac = parts[1]
		if frac == "" {
			return 0, msgAmountFormat
		}
	}
	if whole == "" || !digitsOnly(whole) || (frac != "" && !digitsOnly(frac)) {
		return 0, msgAmountFormat
	}
	if len(frac) > 2 {
		return 0, msgAmountDecimals
	}
	for len(frac) < 2 {
		frac += "0"
	}
	whole = strings.TrimLeft(whole, "0")
	if whole == "" {
		whole = "0"
	}
	if len(whole) > 12 {
		if neg {
			return 0, msgAmountPositive
		}
		return 0, msgAmountLimit
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		if neg {
			return 0, msgAmountPositive
		}
		return 0, msgAmountLimit
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, msgAmountFormat
	}
	if neg || (w == 0 && f == 0) {
		return 0, msgAmountPositive
	}
	if w > maxYuan || (w == maxYuan && f > 0) {
		return 0, msgAmountLimit
	}
	return w*100 + f, ""
}

func digitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
