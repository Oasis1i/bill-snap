package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Oasis1i/bill-snap/internal/extract"
	"github.com/Oasis1i/bill-snap/internal/store"
	"github.com/Oasis1i/bill-snap/internal/testimg"
	_ "modernc.org/sqlite"
)

func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bills.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	upload := filepath.Join(dir, "uploads")
	srv := New(Options{
		Token:          "test-token",
		UploadDir:      upload,
		Location:       loc,
		Store:          st,
		Extractor:      extract.NewMock(),
		MaxUploadBytes: 1 << 20,
	})
	return srv, upload
}

func uploadReq(t *testing.T, filename string, data []byte, token string, header string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("image", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/upload", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	if token != "" {
		if header == "x" {
			req.Header.Set("X-API-Token", token)
		} else {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	return req
}

func TestUploadQueryAndAuth(t *testing.T) {
	srv, upload := newTestServer(t)
	h := srv.Handler()

	health := httptest.NewRecorder()
	h.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatal(health.Body.String())
	}

	noisy, err := testimg.NoisyPNG(120, 90, 2)
	if err != nil {
		t.Fatal(err)
	}
	blank, err := testimg.SolidPNG(80, 80)
	if err != nil {
		t.Fatal(err)
	}

	noAuth := httptest.NewRecorder()
	h.ServeHTTP(noAuth, uploadReq(t, "bill.png", noisy, "", ""))
	if noAuth.Code != http.StatusUnauthorized {
		t.Fatalf("auth: %d %s", noAuth.Code, noAuth.Body.String())
	}

	badType := httptest.NewRecorder()
	h.ServeHTTP(badType, uploadReq(t, "note.txt", []byte("hello"), "test-token", ""))
	if badType.Code != http.StatusBadRequest {
		t.Fatalf("type: %d %s", badType.Code, badType.Body.String())
	}

	createdRec := httptest.NewRecorder()
	h.ServeHTTP(createdRec, uploadReq(t, "bill.png", noisy, "test-token", ""))
	if createdRec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", createdRec.Code, createdRec.Body.String())
	}
	var created uploadResponse
	if err := json.Unmarshal(createdRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !created.IsBill || created.Duplicate || !created.Stored || created.Bill == nil || created.Bill.ID == 0 {
		t.Fatalf("%+v", created)
	}
	yuan := fmt.Sprintf("%d.%02d", created.Bill.AmountCents/100, created.Bill.AmountCents%100)
	if created.Bill.Amount != yuan || created.Bill.Category == "" {
		t.Fatalf("yuan amount: %+v", created.Bill)
	}
	if created.Bill.Note != "" || created.Bill.RawModelJSON != "" || strings.Contains(createdRec.Body.String(), "mock extractor") {
		t.Fatalf("english note leaked: %s", createdRec.Body.String())
	}
	if !strings.Contains(createdRec.Body.String(), `"amount":"`+yuan+`"`) {
		t.Fatalf("amount field missing: %s", createdRec.Body.String())
	}

	dupRec := httptest.NewRecorder()
	h.ServeHTTP(dupRec, uploadReq(t, "bill.png", noisy, "test-token", "x"))
	if dupRec.Code != http.StatusOK {
		t.Fatalf("dup: %d %s", dupRec.Code, dupRec.Body.String())
	}
	var dup uploadResponse
	if err := json.Unmarshal(dupRec.Body.Bytes(), &dup); err != nil {
		t.Fatal(err)
	}
	if !dup.Duplicate || dup.Stored || dup.Bill.ID != created.Bill.ID {
		t.Fatalf("%+v", dup)
	}
	if dup.Bill.Amount != created.Bill.Amount || dup.Bill.Category != created.Bill.Category {
		t.Fatalf("duplicate yuan: %+v", dup.Bill)
	}

	blankRec := httptest.NewRecorder()
	h.ServeHTTP(blankRec, uploadReq(t, "blank.png", blank, "test-token", ""))
	if blankRec.Code != http.StatusOK {
		t.Fatalf("blank: %d %s", blankRec.Code, blankRec.Body.String())
	}
	var discarded uploadResponse
	if err := json.Unmarshal(blankRec.Body.Bytes(), &discarded); err != nil {
		t.Fatal(err)
	}
	if discarded.IsBill || discarded.Stored || discarded.Reason != "图片几乎是纯色" || discarded.Notes != discarded.Reason {
		t.Fatalf("%+v", discarded)
	}
	if asciiLetters.MatchString(discarded.Reason) || strings.Contains(blankRec.Body.String(), "solid") || strings.Contains(blankRec.Body.String(), "receipt") {
		t.Fatalf("english reason leaked: %s", blankRec.Body.String())
	}

	entries, err := os.ReadDir(upload)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("upload dir has %d files, want 1", len(entries))
	}

	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/bills?from=2026-01-01&to=2026-12-31&category="+created.Bill.Category, nil)
	listReq.Header.Set("Authorization", "Bearer test-token")
	h.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatal(listRec.Body.String())
	}
	var listed struct {
		Count int `json:"count"`
		Bills []struct {
			ID           int64  `json:"id"`
			Amount       string `json:"amount"`
			Note         string `json:"note"`
			Category     string `json:"category"`
			RawModelJSON string `json:"raw_model_json"`
		} `json:"bills"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Count != 1 || listed.Bills[0].ID != created.Bill.ID {
		t.Fatalf("%+v", listed)
	}
	if listed.Bills[0].Amount != created.Bill.Amount || listed.Bills[0].Category != created.Bill.Category || listed.Bills[0].Note != "" || listed.Bills[0].RawModelJSON != "" {
		t.Fatalf("list bill: %+v", listed.Bills[0])
	}

	sumRec := httptest.NewRecorder()
	sumReq := httptest.NewRequest(http.MethodGet, "/api/v1/summary?from=2026-01-01&to=2026-12-31&category="+created.Bill.Category, nil)
	sumReq.Header.Set("X-API-Token", "test-token")
	h.ServeHTTP(sumRec, sumReq)
	if sumRec.Code != http.StatusOK {
		t.Fatal(sumRec.Body.String())
	}
	var sum struct {
		Count  int `json:"count"`
		Totals []struct {
			AmountCents int64 `json:"amount_cents"`
		} `json:"totals"`
		ByCategory []struct {
			Category string `json:"category"`
		} `json:"by_category"`
	}
	if err := json.Unmarshal(sumRec.Body.Bytes(), &sum); err != nil {
		t.Fatal(err)
	}
	if sum.Count != 1 || sum.Totals[0].AmountCents != created.Bill.AmountCents || sum.ByCategory[0].Category != created.Bill.Category {
		t.Fatalf("%s", sumRec.Body.String())
	}

	badLimit := httptest.NewRecorder()
	badReq := httptest.NewRequest(http.MethodGet, "/api/v1/bills?limit=9999", nil)
	badReq.Header.Set("Authorization", "Bearer test-token")
	h.ServeHTTP(badLimit, badReq)
	if badLimit.Code != http.StatusBadRequest {
		t.Fatal(badLimit.Body.String())
	}

	wrong := httptest.NewRecorder()
	wrongReq := httptest.NewRequest(http.MethodGet, "/api/v1/summary", nil)
	wrongReq.Header.Set("Authorization", "Basic test-token")
	h.ServeHTTP(wrong, wrongReq)
	if wrong.Code != http.StatusUnauthorized {
		t.Fatal(wrong.Body.String())
	}
}

type seqExtractor struct {
	i   int
	res []extract.Result
}

func (s *seqExtractor) Extract(context.Context, []byte, string) (extract.Result, error) {
	if s.i >= len(s.res) {
		return extract.Result{}, fmt.Errorf("unexpected extract call")
	}
	out := s.res[s.i]
	s.i++
	return out, nil
}

func TestUploadOrderAndDefaultedDedup(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bills.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	paid := time.Date(2026, 10, 26, 12, 9, 0, 0, loc)
	later := time.Date(2026, 10, 26, 18, 40, 0, 0, loc)
	fuzzy := time.Date(2026, 10, 26, 20, 5, 0, 0, loc)
	ex := &seqExtractor{res: []extract.Result{
		{
			IsBill: true, AmountCents: 1070, Currency: "CNY", Merchant: "沙县小吃",
			Category: "餐饮", PaidAt: paid, OrderNo: "WX-4200 123",
		},
		{
			IsBill: true, AmountCents: 1070, Currency: "CNY", Merchant: "别的店名",
			Category: "餐饮", PaidAt: later, OrderNo: "wx4200123",
		},
		{
			IsBill: true, AmountCents: 1070, Currency: "CNY", Merchant: "沙县小吃",
			Category: "餐饮", PaidAt: later, PaidAtDefaulted: true, OrderNo: "OTHER-99",
		},
		{
			IsBill: true, AmountCents: 1070, Currency: "CNY", Merchant: " 沙县小吃 ",
			Category: "餐饮", PaidAt: fuzzy, PaidAtDefaulted: true,
		},
	}}
	upload := filepath.Join(dir, "uploads")
	srv := New(Options{
		Token:          "test-token",
		UploadDir:      upload,
		Location:       loc,
		Store:          st,
		Extractor:      ex,
		MaxUploadBytes: 1 << 20,
	})
	h := srv.Handler()

	var images [][]byte
	for seed := 21; seed <= 24; seed++ {
		img, err := testimg.NoisyPNG(96, 96, seed)
		if err != nil {
			t.Fatal(err)
		}
		images = append(images, img)
	}
	post := func(img []byte) uploadResponse {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, uploadReq(t, "bill.png", img, "test-token", ""))
		var body uploadResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		bodyCode := rec.Code
		if body.Bill == nil {
			t.Fatalf("status %d %+v", bodyCode, body)
		}
		return body
	}

	first := post(images[0])
	if first.Duplicate || !first.Stored || first.Bill.OrderNo != "WX4200123" || first.Bill.PaidAtDefaulted {
		t.Fatalf("%+v", first)
	}
	sameOrder := post(images[1])
	if !sameOrder.Duplicate || sameOrder.Stored || sameOrder.Bill.ID != first.Bill.ID {
		t.Fatalf("same order number: %+v", sameOrder)
	}
	otherOrder := post(images[2])
	if otherOrder.Duplicate || !otherOrder.Stored || otherOrder.Bill.ID == first.Bill.ID || otherOrder.Bill.OrderNo != "OTHER99" {
		t.Fatalf("distinct order: %+v", otherOrder)
	}
	defaulted := post(images[3])
	if !defaulted.Duplicate || defaulted.Stored || defaulted.Bill.ID != first.Bill.ID {
		t.Fatalf("defaulted same day: %+v", defaulted)
	}

	entries, err := os.ReadDir(upload)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("upload dir has %d files, want 2 (duplicates must not keep a second image)", len(entries))
	}
}

func TestUploadFailureMessages(t *testing.T) {
	noisy, err := testimg.NoisyPNG(96, 80, 4)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "sk-LIVEKEY1234567890"

	t.Run("unauthorized", func(t *testing.T) {
		srv, _ := newTestServer(t)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, uploadReq(t, "a.png", noisy, "", ""))
		assertAPIError(t, rec, http.StatusUnauthorized, "unauthorized", "未授权")
	})

	t.Run("too large", func(t *testing.T) {
		srv := serverWith(t, extract.NewMock(), 32)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, uploadReq(t, "a.png", noisy, "test-token", ""))
		assertAPIError(t, rec, http.StatusRequestEntityTooLarge, "image_too_large", "图片超过上传大小限制")
	})

	t.Run("bad type", func(t *testing.T) {
		srv, _ := newTestServer(t)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, uploadReq(t, "note.txt", []byte("hello"), "test-token", ""))
		assertAPIError(t, rec, http.StatusBadRequest, "bad_image_type", "图片格式不支持")
	})

	t.Run("empty image", func(t *testing.T) {
		srv, _ := newTestServer(t)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, uploadReq(t, "a.png", nil, "test-token", ""))
		assertAPIError(t, rec, http.StatusBadRequest, "empty_image", "图片是空的")
	})

	t.Run("missing image", func(t *testing.T) {
		srv, _ := newTestServer(t)
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		if err := w.WriteField("note", "x"); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/upload", &buf)
		req.Header.Set("Content-Type", w.FormDataContentType())
		req.Header.Set("Authorization", "Bearer test-token")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		assertAPIError(t, rec, http.StatusBadRequest, "missing_image", "缺少图片")
	})

	t.Run("invalid form", func(t *testing.T) {
		srv, _ := newTestServer(t)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/upload", strings.NewReader("nope"))
		req.Header.Set("Content-Type", "multipart/form-data; boundary=none")
		req.Header.Set("Authorization", "Bearer test-token")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		assertAPIError(t, rec, http.StatusBadRequest, "invalid_form", "上传格式不对")
	})

	extractCases := []struct {
		name    string
		err     error
		code    string
		message string
	}{
		{"timeout", &extract.Failure{Kind: extract.FailureTimeout, Detail: "context deadline exceeded"}, "extract_timeout", "识别超时"},
		{"deadline", context.DeadlineExceeded, "extract_timeout", "识别超时"},
		{"bad response", &extract.Failure{Kind: extract.FailureBadResponse, Detail: "model response JSON is invalid"}, "extract_bad_response", "识别结果无法解析"},
		{"provider", &extract.Failure{Kind: extract.FailureProvider, Provider: "qwen", Detail: "HTTP 502: " + secret + " " + strings.Repeat("Z", 3000)}, "extract_provider_error", "识别服务出错"},
		{"not configured", &extract.Failure{Kind: extract.FailureNotConfigured, Detail: "vision API key or model is empty"}, "extract_not_configured", "识别服务未配置"},
		{"other", fmt.Errorf("exploded: %s %s", secret, strings.Repeat("Q", 3000)), "extract_failed", "识别失败"},
	}
	for _, tc := range extractCases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			srv := serverWith(t, errExtractor{err: tc.err}, 1<<20)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, uploadReq(t, "a.png", noisy, "test-token", ""))
			assertAPIError(t, rec, http.StatusBadGateway, tc.code, tc.message)
			body := rec.Body.String()
			if strings.Contains(body, secret) || strings.Contains(body, strings.Repeat("Z", 40)) || strings.Contains(body, strings.Repeat("Q", 40)) {
				t.Fatalf("response leaked provider detail: %s", body)
			}
			line := logs.String()
			if strings.Contains(line, secret) {
				t.Fatalf("log leaked secret: %s", line)
			}
			if strings.Contains(line, strings.Repeat("Z", 250)) || strings.Contains(line, strings.Repeat("Q", 250)) {
				t.Fatalf("log kept a long payload: %s", line)
			}
		})
	}

	t.Run("missing paid_at", func(t *testing.T) {
		srv := serverWith(t, fixedExtractor{res: extract.Result{
			IsBill: true, AmountCents: 100, Currency: "CNY", Merchant: "店",
		}}, 1<<20)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, uploadReq(t, "a.png", noisy, "test-token", ""))
		assertAPIError(t, rec, http.StatusUnprocessableEntity, "missing_paid_at", "没识别出支付时间")
	})

	t.Run("negative amount", func(t *testing.T) {
		srv := serverWith(t, fixedExtractor{res: extract.Result{
			IsBill: true, AmountCents: -50, Currency: "CNY", Merchant: "店", PaidAt: time.Now(),
		}}, 1<<20)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, uploadReq(t, "a.png", noisy, "test-token", ""))
		assertAPIError(t, rec, http.StatusUnprocessableEntity, "negative_amount", "金额不能为负数")
	})

	t.Run("empty notes", func(t *testing.T) {
		srv := serverWith(t, fixedExtractor{res: extract.Result{IsBill: false}}, 1<<20)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, uploadReq(t, "a.png", noisy, "test-token", ""))
		if rec.Code != http.StatusOK {
			t.Fatal(rec.Body.String())
		}
		var body uploadResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.IsBill || body.Reason != "这张图不是付款账单" || body.Notes != body.Reason {
			t.Fatalf("%+v", body)
		}
		if asciiLetters.MatchString(body.Reason) {
			t.Fatalf("english reason: %+v", body)
		}
	})

	t.Run("notes cut at comma", func(t *testing.T) {
		srv := serverWith(t, fixedExtractor{res: extract.Result{IsBill: false, Notes: "聊天记录，不是付款账单。"}}, 1<<20)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, uploadReq(t, "a.png", noisy, "test-token", ""))
		var body uploadResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Reason != "聊天记录" || strings.Contains(body.Reason, "。") || strings.Contains(body.Reason, "，") {
			t.Fatalf("%+v", body)
		}
	})

	t.Run("model notes stay short", func(t *testing.T) {
		notes := "聊天截图 " + secret + " " + strings.Repeat("哈", 100)
		srv := serverWith(t, fixedExtractor{res: extract.Result{IsBill: false, Notes: notes}}, 1<<20)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, uploadReq(t, "a.png", noisy, "test-token", ""))
		var body uploadResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(body.Reason, secret) {
			t.Fatalf("reason leaked key: %s", body.Reason)
		}
		if utf8.RuneCountInString(body.Reason) > reasonRunes+1 {
			t.Fatalf("reason too long: %s", body.Reason)
		}
		if !strings.HasPrefix(body.Reason, "聊天截图") || body.Notes != body.Reason || asciiLetters.MatchString(body.Reason) {
			t.Fatalf("%+v", body)
		}
		if !strings.Contains(body.Reason, "哈") || utf8.RuneCountInString(body.Reason) > reasonRunes+1 {
			t.Fatalf("%+v", body)
		}
	})

	t.Run("english screenshot reason", func(t *testing.T) {
		srv := serverWith(t, fixedExtractor{res: extract.Result{IsBill: false, Notes: "This is a screenshot, not a bill."}}, 1<<20)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, uploadReq(t, "a.png", noisy, "test-token", ""))
		var body uploadResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Reason != "这张图不是付款账单" || body.Notes != body.Reason || asciiLetters.MatchString(body.Reason) {
			t.Fatalf("%+v", body)
		}
		if strings.Contains(strings.ToLower(rec.Body.String()), "screenshot") {
			t.Fatalf("english reason leaked: %s", rec.Body.String())
		}
	})

	t.Run("defaulted note is chinese", func(t *testing.T) {
		srv := serverWith(t, fixedExtractor{res: extract.Result{
			IsBill: true, AmountCents: 2590, Currency: "CNY", Merchant: "沙县小吃",
			Category: "餐饮", PaidAt: time.Now(), PaidAtDefaulted: true,
			Notes: "paid_at missing; defaulted to upload time",
		}}, 1<<20)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, uploadReq(t, "a.png", noisy, "test-token", ""))
		if rec.Code != http.StatusCreated {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		var body uploadResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Bill == nil || body.Bill.Amount != "25.90" || body.Bill.Category != "餐饮" || body.Bill.Note != "未识别到支付时间" {
			t.Fatalf("%+v", body.Bill)
		}
		if asciiLetters.MatchString(body.Bill.Note) || body.Bill.Merchant != "沙县小吃" {
			t.Fatalf("note or merchant: %+v", body.Bill)
		}
	})

	t.Run("items stay on the bill and the note stays chinese", func(t *testing.T) {
		srv := serverWith(t, fixedExtractor{res: extract.Result{
			IsBill: true, AmountCents: 2590, Currency: "CNY", Merchant: "瑞幸咖啡",
			Category: "餐饮", PaidAt: time.Date(2026, 10, 26, 4, 0, 0, 0, time.UTC),
			Items: "瑞幸生椰拿铁 ×2",
			Notes: "paid_at missing; defaulted to upload time",
		}}, 1<<20)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, uploadReq(t, "a.png", noisy, "test-token", ""))
		if rec.Code != http.StatusCreated {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
		var body uploadResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Bill == nil || body.Bill.Amount != "25.90" || body.Bill.Category != "餐饮" || body.Bill.Items != "瑞幸生椰拿铁 ×2" || body.Bill.Source != "screenshot" {
			t.Fatalf("%+v", body.Bill)
		}
		if body.Bill.Note != "未识别到支付时间" || asciiLetters.MatchString(body.Bill.Note) || strings.Contains(rec.Body.String(), "paid_at missing") {
			t.Fatalf("note leaked english: %s", rec.Body.String())
		}
	})

	t.Run("small image reason", func(t *testing.T) {
		small, err := testimg.NoisyPNG(32, 32, 1)
		if err != nil {
			t.Fatal(err)
		}
		srv, _ := newTestServer(t)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, uploadReq(t, "a.png", small, "test-token", ""))
		var body uploadResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Reason != "图片太小" {
			t.Fatalf("%+v", body)
		}
	})

	t.Run("store read", func(t *testing.T) {
		dir := t.TempDir()
		st, err := store.Open(filepath.Join(dir, "bills.db"))
		if err != nil {
			t.Fatal(err)
		}
		srv := New(Options{
			Token: "test-token", UploadDir: filepath.Join(dir, "uploads"),
			Store: st, Extractor: extract.NewMock(), MaxUploadBytes: 1 << 20,
		})
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, uploadReq(t, "a.png", noisy, "test-token", ""))
		assertAPIError(t, rec, http.StatusInternalServerError, "store_read_failed", "读取账单失败")
		if strings.Contains(rec.Body.String(), "bills.db") || strings.Contains(rec.Body.String(), "sqlite") {
			t.Fatalf("response leaked store error: %s", rec.Body.String())
		}
	})

	t.Run("image save", func(t *testing.T) {
		dir := t.TempDir()
		blocked := filepath.Join(dir, "not-a-dir")
		if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		st, err := store.Open(filepath.Join(dir, "bills.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		srv := New(Options{
			Token: "test-token", UploadDir: blocked,
			Store: st, Extractor: extract.NewMock(), MaxUploadBytes: 1 << 20,
		})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, uploadReq(t, "a.png", noisy, "test-token", ""))
		assertAPIError(t, rec, http.StatusInternalServerError, "image_save_failed", "保存图片失败")
		if strings.Contains(rec.Body.String(), "not-a-dir") {
			t.Fatalf("response leaked path: %s", rec.Body.String())
		}
	})

	t.Run("store write", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "bills.db")
		st, err := store.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if _, err := db.Exec(`CREATE TRIGGER fail_insert BEFORE INSERT ON bills BEGIN SELECT RAISE(FAIL, 'blocked'); END;`); err != nil {
			t.Fatal(err)
		}
		srv := New(Options{
			Token: "test-token", UploadDir: filepath.Join(dir, "uploads"),
			Store: st, Extractor: extract.NewMock(), MaxUploadBytes: 1 << 20,
		})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, uploadReq(t, "a.png", noisy, "test-token", ""))
		assertAPIError(t, rec, http.StatusInternalServerError, "store_write_failed", "保存账单失败")
		if strings.Contains(rec.Body.String(), "blocked") {
			t.Fatalf("response leaked insert error: %s", rec.Body.String())
		}
	})

	t.Run("bad limit", func(t *testing.T) {
		srv, _ := newTestServer(t)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/bills?limit=9999", nil)
		req.Header.Set("Authorization", "Bearer test-token")
		srv.Handler().ServeHTTP(rec, req)
		assertAPIError(t, rec, http.StatusBadRequest, "bad_limit", "条数应为 1 到 500 的整数")
	})

	t.Run("bad date", func(t *testing.T) {
		srv, _ := newTestServer(t)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/bills?from=yesterday", nil)
		req.Header.Set("Authorization", "Bearer test-token")
		srv.Handler().ServeHTTP(rec, req)
		assertAPIError(t, rec, http.StatusBadRequest, "bad_date", "日期格式不对")
	})
}

type errExtractor struct{ err error }

func (e errExtractor) Extract(context.Context, []byte, string) (extract.Result, error) {
	return extract.Result{}, e.err
}

type fixedExtractor struct{ res extract.Result }

func (e fixedExtractor) Extract(context.Context, []byte, string) (extract.Result, error) {
	return e.res, nil
}

func serverWith(t *testing.T, ex extract.Extractor, max int64) *Server {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bills.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(Options{
		Token:          "test-token",
		UploadDir:      filepath.Join(dir, "uploads"),
		Store:          st,
		Extractor:      ex,
		MaxUploadBytes: max,
	})
}

func assertAPIError(t *testing.T, rec *httptest.ResponseRecorder, status int, code, message string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 2 {
		t.Fatalf("want error and message only: %s", rec.Body.String())
	}
	var got apiError
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error != code || got.Message != message {
		t.Fatalf("got %+v want %s %s", got, code, message)
	}
	if strings.ContainsAny(got.Message, "\n\r") || strings.Contains(got.Message, "，") || strings.Contains(got.Message, "。") || strings.Contains(got.Message, ".") || utf8.RuneCountInString(got.Message) > 40 {
		t.Fatalf("message not a short phrase: %q", got.Message)
	}
	if !hasHan(got.Message) || asciiLetters.MatchString(got.Message) {
		t.Fatalf("message is not short zh-Hans: %q", got.Message)
	}
	if strings.Contains(rec.Body.String(), `\u`) {
		t.Fatalf("message was escaped: %s", rec.Body.String())
	}
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}
