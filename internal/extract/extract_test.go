package extract

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Oasis1i/bill-snap/internal/model"
	"github.com/Oasis1i/bill-snap/internal/testimg"
)

func TestMockRules(t *testing.T) {
	ctx := context.Background()
	m := NewMock()

	noisy, err := testimg.NoisyPNG(128, 128, 3)
	if err != nil {
		t.Fatal(err)
	}
	a, err := m.Extract(ctx, noisy, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if !a.IsBill || a.Currency != "CNY" || a.AmountCents < 100 || a.AmountCents > 20000 {
		t.Fatalf("%+v", a)
	}
	if a.PaidAt.Year() != 2026 || a.PaidAt.Minute() != 0 || a.PaidAt.Location() != time.UTC {
		t.Fatalf("paid_at %s", a.PaidAt)
	}
	if a.PaidAtDefaulted || len(a.OrderNo) != 13 || !strings.HasPrefix(a.OrderNo, "M") {
		t.Fatalf("order %q defaulted %v", a.OrderNo, a.PaidAtDefaulted)
	}
	b, err := m.Extract(ctx, noisy, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if a.AmountCents != b.AmountCents || a.Merchant != b.Merchant || !a.PaidAt.Equal(b.PaidAt) || a.Category != b.Category || a.OrderNo != b.OrderNo {
		t.Fatal("mock result was not deterministic")
	}

	small, err := testimg.NoisyPNG(32, 32, 1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Extract(ctx, small, "image/png")
	if err != nil || got.IsBill || !strings.Contains(got.Notes, "64x64") {
		t.Fatalf("%+v %v", got, err)
	}

	blank, err := testimg.SolidPNG(80, 80)
	if err != nil {
		t.Fatal(err)
	}
	got, err = m.Extract(ctx, blank, "image/png")
	if err != nil || got.IsBill || !strings.Contains(got.Notes, "solid") {
		t.Fatalf("%+v %v", got, err)
	}

	got, err = m.Extract(ctx, nil, "image/png")
	if err != nil || got.IsBill {
		t.Fatalf("%+v %v", got, err)
	}
	got, err = m.Extract(ctx, []byte("not an image"), "image/png")
	if err != nil || got.IsBill {
		t.Fatalf("%+v %v", got, err)
	}

	var jpg bytes.Buffer
	img, err := jpeg.Decode(bytes.NewReader(noisy))
	if err != nil {
		// noisy is png; decode then re-encode
		decoded, _, err := image.Decode(bytes.NewReader(noisy))
		if err != nil {
			t.Fatal(err)
		}
		img = decoded
	}
	if err := jpeg.Encode(&jpg, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	got, err = m.Extract(ctx, jpg.Bytes(), "image/jpeg")
	if err != nil || !got.IsBill {
		t.Fatalf("jpeg: %+v %v", got, err)
	}
}

func TestFactory(t *testing.T) {
	ex, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ex.(*Mock); !ok {
		t.Fatalf("%T", ex)
	}
	if _, err := New(Options{Provider: "qwen"}); err == nil {
		t.Fatal("expected missing key error")
	}
	if _, err := New(Options{Provider: "doubao", APIKey: "k"}); err == nil {
		t.Fatal("expected missing model error")
	}
	if _, err := New(Options{Provider: "gpt-4o"}); err == nil {
		t.Fatal("expected unknown provider error")
	}
}

func TestOpenAICompatibleParse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("auth %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if !bytes.Contains(body, []byte("data:image/png;base64,")) {
			t.Errorf("missing data url")
		}
		if !bytes.Contains(body, []byte("order_no")) || !bytes.Contains(body, []byte("交易单号")) {
			t.Errorf("prompt missing order number instructions")
		}
		for _, cat := range model.Categories() {
			if !bytes.Contains(body, []byte(cat)) {
				t.Errorf("prompt missing category %s", cat)
			}
		}
		// The prompt is JSON-encoded in the request body, so quotes are escaped.
		if !bytes.Contains(body, []byte(`\"enum\"`)) || !bytes.Contains(body, []byte("Do not invent a label")) {
			t.Errorf("prompt missing closed category schema")
		}
		inner := `{"is_bill":true,"amount":"18.50","currency":"CNY","merchant":"美团外卖","category":"外卖","paid_at":"2026-10-26T12:34:00+08:00","order_no":" 20261026-001 ","confidence":0.8,"notes":"ok"}`
		writeChoice(w, "```json\n"+inner+"\n```")
	}))
	defer srv.Close()

	ex, err := New(Options{
		Provider:   "tongyi",
		APIKey:     "test-key",
		BaseURL:    srv.URL,
		Model:      "qwen-vl-test",
		HTTPClient: srv.Client(),
		Location:   time.UTC,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := ex.Extract(context.Background(), []byte{1, 2, 3}, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsBill || res.AmountCents != 1850 || res.Merchant != "美团外卖" || res.Category != "外卖" {
		t.Fatalf("%+v", res)
	}
	if res.PaidAt.UTC().Format(time.RFC3339) != "2026-10-26T04:34:00Z" {
		t.Fatal(res.PaidAt)
	}
	if res.PaidAtDefaulted || res.OrderNo != "20261026001" {
		t.Fatalf("order %q defaulted %v", res.OrderNo, res.PaidAtDefaulted)
	}

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeChoice(w, []map[string]string{{"type": "text", "text": `{"is_bill":false,"notes":"chat screenshot"}`}})
	})
	res, err = ex.Extract(context.Background(), []byte{1}, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if res.IsBill || !strings.Contains(res.Notes, "chat") {
		t.Fatalf("%+v", res)
	}

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	})
	if _, err := ex.Extract(context.Background(), []byte{1}, "image/png"); err == nil {
		t.Fatal("expected upstream error")
	}
}

func TestVisionOrderNoAliasesAndDefaultedPaidAt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeChoice(w, `{"is_bill":true,"amount":"10.70","currency":"CNY","merchant":"沙县小吃","category":"餐饮","order_no":"无","交易单号":"WX-4200 123","confidence":0.4}`)
	}))
	defer srv.Close()

	ex, err := New(Options{
		Provider:   "glm",
		APIKey:     "test-key",
		BaseURL:    srv.URL,
		Model:      "glm-test",
		HTTPClient: srv.Client(),
		Location:   time.FixedZone("CST", 8*3600),
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := ex.Extract(context.Background(), []byte{9}, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if !res.PaidAtDefaulted || !strings.Contains(res.Notes, "paid_at missing; defaulted to upload time") {
		t.Fatalf("%+v", res)
	}
	if res.OrderNo != "WX4200123" || res.AmountCents != 1070 {
		t.Fatalf("%+v", res)
	}
	if delta := time.Since(res.PaidAt); delta < -time.Minute || delta > 2*time.Minute {
		t.Fatalf("defaulted paid_at %s", res.PaidAt)
	}

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeChoice(w, `{"is_bill":true,"amount":10.7,"currency":"CNY","merchant":"沙县小吃","category":"餐饮","paid_at":"2026-10-26","order_no":4200001234567890123}`)
	})
	res, err = ex.Extract(context.Background(), []byte{9}, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if res.PaidAtDefaulted || res.OrderNo != "4200001234567890123" {
		t.Fatalf("%+v", res)
	}
	want := time.Date(2026, 10, 26, 12, 0, 0, 0, time.FixedZone("CST", 8*3600))
	if !res.PaidAt.Equal(want) {
		t.Fatal(res.PaidAt)
	}
}

func TestMockCategoriesStayInEnum(t *testing.T) {
	allowed := map[string]bool{}
	for _, c := range model.Categories() {
		allowed[c] = true
	}
	if len(catalog) == 0 {
		t.Fatal("empty catalog")
	}
	for _, item := range catalog {
		if !allowed[item.Category] {
			t.Fatalf("catalog category %q is not in the fixed list", item.Category)
		}
	}
	m := NewMock()
	for seed := 0; seed < 16; seed++ {
		img, err := testimg.NoisyPNG(96, 96, seed)
		if err != nil {
			t.Fatal(err)
		}
		res, err := m.Extract(context.Background(), img, "image/png")
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsBill || !allowed[res.Category] || model.NormalizeCategory(res.Category) != res.Category {
			t.Fatalf("seed %d: %+v", seed, res)
		}
	}
}

func TestVisionPromptCategoryEnum(t *testing.T) {
	p := visionPrompt()
	const marker = `"enum":`
	i := strings.Index(p, marker)
	if i < 0 {
		t.Fatalf("prompt has no category enum:\n%s", p)
	}
	rest := p[i+len(marker):]
	end := strings.IndexByte(rest, ']')
	if end < 0 {
		t.Fatal(rest)
	}
	var got []string
	if err := json.Unmarshal([]byte(rest[:end+1]), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(model.Categories()) {
		t.Fatalf("prompt enum %v", got)
	}
	for i, c := range model.Categories() {
		if got[i] != c {
			t.Fatalf("prompt enum[%d]=%q want %q", i, got[i], c)
		}
	}
	if !strings.Contains(p, "use "+model.CategoryOther) {
		t.Fatal("prompt should tell the model to use 其他 when unsure")
	}
	if strings.Contains(p, "such as") {
		t.Fatal("prompt still invites a free-form category")
	}
}

func TestConfiguredCategoryInPromptAndParse(t *testing.T) {
	model.UseCategories([]string{"宠物", "外卖"})
	t.Cleanup(func() { model.UseCategories(model.DefaultCategories()) })

	p := visionPrompt()
	for _, keep := range []string{"宠物", "外卖", "其他"} {
		if !strings.Contains(p, keep) {
			t.Fatalf("prompt missing %s\n%s", keep, p)
		}
	}
	for _, drop := range []string{"餐饮", "工资", "房租"} {
		if strings.Contains(p, drop) {
			t.Fatalf("prompt still lists %s\n%s", drop, p)
		}
	}
	loc := time.UTC
	res, err := resultFromModelJSON("test", json.RawMessage(`{"is_bill":true,"amount":"1.00","category":"宠物","paid_at":"2026-10-26"}`), loc)
	if err != nil || !res.IsBill || res.Category != "宠物" {
		t.Fatalf("%+v %v", res, err)
	}
	res, err = resultFromModelJSON("test", json.RawMessage(`{"is_bill":true,"amount":"1.00","category":"工资","paid_at":"2026-10-26"}`), loc)
	if err != nil || !res.IsBill || res.Category != "其他" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestNormalizeExtractedCategory(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	cases := []struct {
		raw  string
		want string
		bill bool
	}{
		{`{"is_bill":true,"amount":"1.00","category":" 外卖 ","paid_at":"2026-10-26T12:00:00+08:00"}`, "外卖", true},
		{`{"is_bill":true,"amount":"1.00","category":"","paid_at":"2026-10-26"}`, "其他", true},
		{`{"is_bill":true,"amount":"1.00","paid_at":"2026-10-26"}`, "其他", true},
		{`{"is_bill":true,"amount":"1.00","category":null,"paid_at":"2026-10-26"}`, "其他", true},
		{`{"is_bill":true,"amount":"1.00","category":123,"paid_at":"2026-10-26"}`, "其他", true},
		{`{"is_bill":true,"amount":"1.00","category":"外卖配送","paid_at":"2026-10-26"}`, "其他", true},
		{`{"is_bill":true,"amount":"1.00","category":"外 卖","paid_at":"2026-10-26"}`, "其他", true},
		{`{"is_bill":true,"amount":"1.00","category":"waimai","paid_at":"2026-10-26"}`, "其他", true},
		{`{"is_bill":true,"amount":"1.00","category":" 房租 ","paid_at":"2026-10-26"}`, "房租", true},
		{`{"is_bill":true,"amount":"1.00","category":" 工资","paid_at":"2026-10-26"}`, "工资", true},
		{`{"is_bill":false,"category":"随便"}`, "其他", false},
	}
	for _, tc := range cases {
		res, err := resultFromModelJSON("test", json.RawMessage(tc.raw), loc)
		if err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		if res.IsBill != tc.bill || res.Category != tc.want {
			t.Fatalf("%s: is_bill %v category %q", tc.raw, res.IsBill, res.Category)
		}
	}
}

func writeChoice(w http.ResponseWriter, content any) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
	})
}
