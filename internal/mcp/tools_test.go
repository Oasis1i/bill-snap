package mcp

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestResolveLimit(t *testing.T) {
	n, msg := resolveLimit(nil)
	if n != 50 || msg != "" {
		t.Fatalf("default %d %q", n, msg)
	}
	n, msg = resolveLimit(json.RawMessage("50"))
	if n != 50 || msg != "" {
		t.Fatalf("50 %d %q", n, msg)
	}
	n, msg = resolveLimit(json.RawMessage("201"))
	if n != 200 || msg != "" {
		t.Fatalf("clamp %d %q", n, msg)
	}
	n, msg = resolveLimit(json.RawMessage("1"))
	if n != 1 || msg != "" {
		t.Fatalf("one %d %q", n, msg)
	}
	for _, raw := range []string{"0", "-1", "1.5", `"10"`, "true"} {
		if _, msg := resolveLimit(json.RawMessage(raw)); msg != msgBadLimit {
			t.Fatalf("%s -> %q", raw, msg)
		}
	}
}

func TestParseDateRange(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	if _, msg := parseDateRange("", "2026-10-01", loc); msg != msgMissingFrom {
		t.Fatal(msg)
	}
	if _, msg := parseDateRange("2026-10-01", "", loc); msg != msgMissingTo {
		t.Fatal(msg)
	}
	for _, from := range []string{"2026/10/01", "2026-10-1", "2026-13-01", "2026-10-01T00:00:00Z", "not-a-date"} {
		if _, msg := parseDateRange(from, "2026-10-02", loc); msg != msgBadDate {
			t.Fatalf("%s -> %q", from, msg)
		}
	}
	if _, msg := parseDateRange("2026-10-31", "2026-10-01", loc); msg != msgRangeOrder {
		t.Fatal(msg)
	}
	if _, msg := parseDateRange("2024-01-01", "2024-12-31", loc); msg != "" {
		t.Fatalf("leap year %q", msg)
	}
	if _, msg := parseDateRange("2023-01-01", "2024-01-01", loc); msg != "" {
		t.Fatalf("365 apart %q", msg)
	}
	if _, msg := parseDateRange("2024-01-01", "2025-01-01", loc); msg != msgSpan {
		t.Fatalf("too long %q", msg)
	}
	rng, msg := parseDateRange("2026-10-26", "2026-10-26", loc)
	if msg != "" {
		t.Fatal(msg)
	}
	if !rng.start.Equal(time.Date(2026, 10, 25, 16, 0, 0, 0, time.UTC)) {
		t.Fatal(rng.start)
	}
	if !rng.end.Equal(time.Date(2026, 10, 26, 16, 0, 0, 0, time.UTC)) {
		t.Fatal(rng.end)
	}
}

func TestClientErrorPhrases(t *testing.T) {
	phrases := []string{
		msgBadDate, msgMissingFrom, msgMissingTo, msgRangeOrder, msgSpan,
		msgBadLimit, msgBadGroup, msgBadArgs, msgUnknown, msgQuery,
		msgBadID, msgBillMissing, msgNoImage, msgImageGone, msgImageBad,
		"未知分类 允许" + strings.Join(allowedLabels(), "、"),
		"请求无法解析", "请求格式不对", "方法不存在", "服务器出错",
		"协议版本不支持", "请求过大", "接口未启用", "只支持POST", "未授权",
		"不接受该响应类型", "内容类型不支持",
	}
	for _, p := range phrases {
		if p == "" || strings.Contains(p, "，") || strings.Contains(p, ",") || strings.Contains(p, "。") || strings.HasSuffix(p, ".") {
			t.Fatalf("phrase %q", p)
		}
	}
}

func TestResolveGroupAndCategory(t *testing.T) {
	g, msg := resolveGroupBy("")
	if g != "category" || msg != "" {
		t.Fatalf("%s %s", g, msg)
	}
	g, msg = resolveGroupBy("day")
	if g != "day" || msg != "" {
		t.Fatalf("%s %s", g, msg)
	}
	if _, msg = resolveGroupBy("week"); msg != msgBadGroup {
		t.Fatal(msg)
	}
	if _, msg = resolveCategory("不存在"); !strings.HasPrefix(msg, "未知分类 允许") || !strings.Contains(msg, "外卖") || !strings.Contains(msg, "工资") {
		t.Fatal(msg)
	}
	cat, msg := resolveCategory(" 外卖 ")
	if cat != "外卖" || msg != "" {
		t.Fatalf("%s %s", cat, msg)
	}
}
