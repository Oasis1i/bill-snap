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
	b, err := m.Extract(ctx, noisy, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if a.AmountCents != b.AmountCents || a.Merchant != b.Merchant || !a.PaidAt.Equal(b.PaidAt) || a.Category != b.Category {
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
		inner := `{"is_bill":true,"amount":"18.50","currency":"CNY","merchant":"美团外卖","category":"外卖","paid_at":"2026-10-26T12:34:00+08:00","confidence":0.8,"notes":"ok"}`
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

func writeChoice(w http.ResponseWriter, content any) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
	})
}
