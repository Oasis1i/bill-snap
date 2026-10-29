package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/Oasis1i/bill-snap/internal/model"
	"github.com/Oasis1i/bill-snap/internal/store"
)

const (
	// incomeCategory is left out of summarize_spend unless include_income is true.
	incomeCategory = "工资"

	defaultLimit = 50
	maxLimit     = 200
	maxSpanDays  = 366

	msgBadDate     = "日期格式不对"
	msgMissingFrom = "缺少开始日期"
	msgMissingTo   = "缺少结束日期"
	msgRangeOrder  = "开始日期不能晚于结束日期"
	msgSpan        = "日期跨度不能超过366天"
	msgBadLimit    = "条数应为1到200的整数"
	msgBadGroup    = "汇总方式只能是category或day"
	msgBadArgs     = "参数不正确"
	msgUnknown     = "未知工具"
	msgQuery       = "查询账单失败"
	msgBadID       = "参数 id 无效"
	msgBillMissing = "找不到这笔账单"
	msgNoImage     = "这笔没有截图"
	msgImageGone   = "截图文件已丢失"
	msgImageBad    = "截图无法读取"
)

func toolDefs(write bool) []map[string]any {
	cats := model.Categories()
	tools := []map[string]any{
		{
			"name":        "list_bills",
			"description": "按日期范围列出账单明细，金额单位为元，时间按 Asia/Shanghai",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"from": dateProperty("开始日期 含当天 格式为YYYY-MM-DD"),
					"to":   dateProperty("结束日期 含当天 格式为YYYY-MM-DD"),
					"category": map[string]any{
						"type":        "string",
						"enum":        cats,
						"description": "可选 必须是配置的分类之一",
					},
					"limit": map[string]any{
						"type":        "integer",
						"minimum":     1,
						"maximum":     maxLimit,
						"default":     defaultLimit,
						"description": "返回条数 默认50 最大200 超过则按200",
					},
				},
				"required": []string{"from", "to"},
			},
		},
		{
			"name":        "summarize_spend",
			"description": "按日期范围汇总支出，给出总额和各类别金额与笔数",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"from": dateProperty("开始日期 含当天 格式为YYYY-MM-DD"),
					"to":   dateProperty("结束日期 含当天 格式为YYYY-MM-DD"),
					"group_by": map[string]any{
						"type":        "string",
						"enum":        []string{"category", "day"},
						"default":     "category",
						"description": "汇总维度 category或day 默认category",
					},
					"include_income": map[string]any{
						"type":        "boolean",
						"default":     false,
						"description": "是否计入工资 默认false不计入",
					},
				},
				"required": []string{"from", "to"},
			},
		},
		{
			"name":        "list_categories",
			"description": "返回当前配置的分类",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			"name":        "get_bill_image",
			"description": "返回 list_bills 查到的某一笔账单的原始付款截图。id 是该账单编号。图片最长边不超过 1024 像素且不会放大，并附带日期、分类和金额",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "integer",
						"minimum":     1,
						"description": "账单编号 来自 list_bills 返回的 id",
					},
				},
				"required": []string{"id"},
			},
		},
	}
	if write {
		tools = append(tools, addBillTool())
	}
	return tools
}

func dateProperty(desc string) map[string]any {
	return map[string]any{
		"type":        "string",
		"pattern":     `^\d{4}-\d{2}-\d{2}$`,
		"description": desc,
	}
}

type toolContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

type toolResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type billItem struct {
	ID       int64  `json:"id"`
	PaidAt   string `json:"paid_at"`
	Amount   string `json:"amount"`
	Category string `json:"category"`
	Merchant string `json:"merchant"`
	Items    string `json:"items"`
	Note     string `json:"note"`
	Source   string `json:"source"`
	// HasImage is true only when image_path is non-empty and the file is a
	// regular file that still resolves inside the configured upload directory.
	// A missing file or a path outside that directory is false. The path
	// itself is not returned.
	HasImage bool `json:"has_image"`
}

type billListResult struct {
	Count int        `json:"count"`
	Bills []billItem `json:"bills"`
}

type spendGroup struct {
	Category string `json:"category,omitempty"`
	Day      string `json:"day,omitempty"`
	Amount   string `json:"amount"`
	Count    int    `json:"count"`
}

type spendResult struct {
	Amount  string       `json:"amount"`
	Count   int          `json:"count"`
	GroupBy string       `json:"group_by"`
	Groups  []spendGroup `json:"groups"`
}

type categoryResult struct {
	Categories []string `json:"categories"`
}

func callTool(ctx context.Context, st *store.Store, loc *time.Location, uploadDir string, access Access, params json.RawMessage) toolResult {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(params) > 0 && string(params) != "null" {
		if err := json.Unmarshal(params, &call); err != nil {
			return toolError(msgBadArgs)
		}
	}
	if strings.TrimSpace(call.Name) == "" {
		return toolError(msgBadArgs)
	}
	if len(call.Arguments) == 0 || string(call.Arguments) == "null" {
		call.Arguments = []byte(`{}`)
	}
	switch call.Name {
	case "list_bills":
		return listBills(ctx, st, loc, uploadDir, call.Arguments)
	case "summarize_spend":
		return summarizeSpend(ctx, st, loc, call.Arguments)
	case "list_categories":
		return toolJSON(categoryResult{Categories: model.Categories()})
	case "get_bill_image":
		return getBillImage(ctx, st, loc, uploadDir, call.Arguments)
	case "add_bill":
		return addBill(ctx, st, loc, uploadDir, access, call.Arguments, clock())
	default:
		return toolError(msgUnknown)
	}
}

func listBills(ctx context.Context, st *store.Store, loc *time.Location, uploadDir string, raw json.RawMessage) toolResult {
	var args struct {
		From     string          `json:"from"`
		To       string          `json:"to"`
		Category string          `json:"category"`
		Limit    json.RawMessage `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError(msgBadArgs)
	}
	rng, msg := parseDateRange(args.From, args.To, loc)
	if msg != "" {
		return toolError(msg)
	}
	cat, msg := resolveCategory(args.Category)
	if msg != "" {
		return toolError(msg)
	}
	limit, msg := resolveLimit(args.Limit)
	if msg != "" {
		return toolError(msg)
	}
	if st == nil {
		slog.Error("mcp list", "err", "nil store")
		return toolError(msgQuery)
	}
	bills, err := st.List(ctx, store.ListFilter{
		From:     &rng.start,
		To:       &rng.end,
		Category: cat,
		Limit:    limit,
	})
	if err != nil {
		slog.Error("mcp list", "err", err)
		return toolError(msgQuery)
	}
	items := make([]billItem, 0, len(bills))
	for _, b := range bills {
		items = append(items, billItem{
			ID:       b.ID,
			PaidAt:   formatPaidAt(b.PaidAt, loc),
			Amount:   yuan(b),
			Category: b.Category,
			Merchant: visibleText(b.Merchant, b.ImagePath, b.RawModelJSON),
			Items:    b.Items,
			Note:     visibleText(b.Note, b.ImagePath, b.RawModelJSON),
			Source:   b.Source,
			HasImage: hasBillImage(uploadDir, b.ImagePath),
		})
	}
	return toolJSON(billListResult{Count: len(items), Bills: items})
}

func getBillImage(ctx context.Context, st *store.Store, loc *time.Location, uploadDir string, raw json.RawMessage) toolResult {
	id, msg := resolveBillID(raw)
	if msg != "" {
		return toolError(msg)
	}
	if st == nil {
		slog.Error("mcp image", "err", "nil store")
		return toolError(msgQuery)
	}
	bill, found, err := st.FindByID(ctx, id)
	if err != nil {
		slog.Error("mcp image", "id", id, "err", err)
		return toolError(msgQuery)
	}
	if !found {
		return toolError(msgBillMissing)
	}
	data, err := readBillImage(uploadDir, bill.ImagePath)
	if err != nil {
		if errors.Is(err, errEscape) {
			slog.Warn("mcp image rejected", "id", id, "reason", "outside upload dir")
		}
		return imageReadError(err)
	}
	encoded, mime, err := encodeBillImage(data)
	if err != nil {
		slog.Error("mcp image decode", "id", id, "err", err)
		return toolError(msgImageBad)
	}
	return toolResult{Content: []toolContent{
		{
			Type:     "image",
			Data:     base64.StdEncoding.EncodeToString(encoded),
			MimeType: mime,
		},
		{Type: "text", Text: imageCaption(bill, loc)},
	}}
}

func imageReadError(err error) toolResult {
	switch {
	case errors.Is(err, errNoImage):
		return toolError(msgNoImage)
	case errors.Is(err, errEscape), errors.Is(err, errMissing):
		return toolError(msgImageGone)
	default:
		return toolError(msgImageBad)
	}
}

func resolveBillID(raw json.RawMessage) (int64, string) {
	var args struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return 0, msgBadID
	}
	idRaw := bytes.TrimSpace(args.ID)
	if len(idRaw) == 0 || string(idRaw) == "null" {
		return 0, msgBadID
	}
	var n int64
	if err := json.Unmarshal(idRaw, &n); err != nil || n < 1 {
		return 0, msgBadID
	}
	return n, ""
}

func imageCaption(b model.Bill, loc *time.Location) string {
	return formatDay(b.PaidAt, loc) + " " + strings.TrimSpace(b.Category) + " " + yuan(b) + " 元"
}

func summarizeSpend(ctx context.Context, st *store.Store, loc *time.Location, raw json.RawMessage) toolResult {
	var args struct {
		From          string          `json:"from"`
		To            string          `json:"to"`
		GroupBy       string          `json:"group_by"`
		IncludeIncome json.RawMessage `json:"include_income"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolError(msgBadArgs)
	}
	rng, msg := parseDateRange(args.From, args.To, loc)
	if msg != "" {
		return toolError(msg)
	}
	groupBy, msg := resolveGroupBy(args.GroupBy)
	if msg != "" {
		return toolError(msg)
	}
	includeIncome, msg := parseBoolDefaultFalse(args.IncludeIncome)
	if msg != "" {
		return toolError(msg)
	}
	f := store.ListFilter{From: &rng.start, To: &rng.end}
	if !includeIncome {
		f.ExcludeCategory = incomeCategory
	}
	if st == nil {
		slog.Error("mcp summarize", "err", "nil store")
		return toolError(msgQuery)
	}
	lines, err := st.SpendLines(ctx, f)
	if err != nil {
		slog.Error("mcp summarize", "err", err)
		return toolError(msgQuery)
	}
	return toolJSON(aggregateSpend(lines, groupBy, loc))
}

func aggregateSpend(lines []store.SpendLine, groupBy string, loc *time.Location) spendResult {
	type bucket struct {
		cents int64
		count int
	}
	groups := map[string]*bucket{}
	keys := make([]string, 0)
	var total int64
	for _, line := range lines {
		key := line.Category
		if groupBy == "day" {
			key = formatDay(line.PaidAt, loc)
		}
		b, ok := groups[key]
		if !ok {
			b = &bucket{}
			groups[key] = b
			keys = append(keys, key)
		}
		b.cents += line.AmountCents
		b.count++
		total += line.AmountCents
	}
	sort.Strings(keys)
	out := spendResult{
		Amount:  model.FormatCents(total),
		Count:   len(lines),
		GroupBy: groupBy,
		Groups:  make([]spendGroup, 0, len(keys)),
	}
	for _, key := range keys {
		b := groups[key]
		item := spendGroup{Amount: model.FormatCents(b.cents), Count: b.count}
		if groupBy == "day" {
			item.Day = key
		} else {
			item.Category = key
		}
		out.Groups = append(out.Groups, item)
	}
	return out
}

type dateRange struct {
	start time.Time
	end   time.Time
}

func parseDateRange(from, to string, loc *time.Location) (dateRange, string) {
	if loc == nil {
		loc = time.UTC
	}
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)
	if from == "" {
		return dateRange{}, msgMissingFrom
	}
	if to == "" {
		return dateRange{}, msgMissingTo
	}
	startDay, ok := parseCivilDate(from, loc)
	if !ok {
		return dateRange{}, msgBadDate
	}
	endDay, ok := parseCivilDate(to, loc)
	if !ok {
		return dateRange{}, msgBadDate
	}
	if startDay.After(endDay) {
		return dateRange{}, msgRangeOrder
	}
	if inclusiveDays(startDay, endDay) > maxSpanDays {
		return dateRange{}, msgSpan
	}
	start := time.Date(startDay.Year(), startDay.Month(), startDay.Day(), 0, 0, 0, 0, loc).UTC()
	end := time.Date(endDay.Year(), endDay.Month(), endDay.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1).UTC()
	return dateRange{start: start, end: end}, ""
}

func parseCivilDate(s string, loc *time.Location) (time.Time, bool) {
	if len(s) != len("2006-01-02") || s[4] != '-' || s[7] != '-' {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006-01-02", s, loc)
	if err != nil || t.Format("2006-01-02") != s {
		return time.Time{}, false
	}
	return t, true
}

func inclusiveDays(from, to time.Time) int {
	a := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	b := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.UTC)
	return int(b.Sub(a).Hours()/24) + 1
}

func resolveCategory(raw string) (string, string) {
	c := strings.TrimSpace(raw)
	if c == "" {
		return "", ""
	}
	for _, item := range model.Categories() {
		if item == c {
			return c, ""
		}
	}
	return "", "未知分类 允许" + strings.Join(allowedLabels(), "、")
}

func allowedLabels() []string {
	cats := model.Categories()
	out := make([]string, len(cats))
	for i, c := range cats {
		c = strings.ReplaceAll(c, ",", " ")
		c = strings.ReplaceAll(c, "，", " ")
		c = strings.ReplaceAll(c, "。", "")
		out[i] = strings.TrimSpace(c)
	}
	return out
}

func resolveLimit(raw json.RawMessage) (int, string) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return defaultLimit, ""
	}
	// Reject strings and fractions. json.Number's underlying type is string,
	// so a JSON string would otherwise decode.
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil || n < 1 {
		return 0, msgBadLimit
	}
	if n > maxLimit {
		return maxLimit, ""
	}
	return int(n), ""
}

func resolveGroupBy(raw string) (string, string) {
	g := strings.TrimSpace(raw)
	if g == "" {
		return "category", ""
	}
	switch g {
	case "category", "day":
		return g, ""
	default:
		return "", msgBadGroup
	}
}

func parseBoolDefaultFalse(raw json.RawMessage) (bool, string) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return false, ""
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, msgBadArgs
	}
	return b, ""
}

func yuan(b model.Bill) string {
	if b.Amount != "" {
		return b.Amount
	}
	return model.FormatCents(b.AmountCents)
}

func formatPaidAt(stored string, loc *time.Location) string {
	t, ok := parseStoredTime(stored)
	if !ok {
		return stored
	}
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02 15:04")
}

func formatDay(stored string, loc *time.Location) string {
	t, ok := parseStoredTime(stored)
	if !ok {
		if len(stored) >= 10 {
			return stored[:10]
		}
		return stored
	}
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02")
}

func parseStoredTime(stored string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, stored); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339Nano, stored); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func visibleText(s string, hidden ...string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, model.PaidAtDefaultedNote, "未识别到支付时间")
	for _, h := range hidden {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		s = strings.ReplaceAll(s, h, "")
	}
	return strings.TrimSpace(s)
}

func toolJSON(v any) toolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return toolError(msgQuery)
	}
	return toolResult{Content: []toolContent{{Type: "text", Text: string(b)}}}
}

func toolError(msg string) toolResult {
	return toolResult{IsError: true, Content: []toolContent{{Type: "text", Text: msg}}}
}
