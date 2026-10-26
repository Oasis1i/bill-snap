package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Oasis1i/bill-snap/internal/extract"
	"github.com/Oasis1i/bill-snap/internal/store"
	"github.com/Oasis1i/bill-snap/internal/testimg"
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

	blankRec := httptest.NewRecorder()
	h.ServeHTTP(blankRec, uploadReq(t, "blank.png", blank, "test-token", ""))
	if blankRec.Code != http.StatusOK {
		t.Fatalf("blank: %d %s", blankRec.Code, blankRec.Body.String())
	}
	var discarded uploadResponse
	if err := json.Unmarshal(blankRec.Body.Bytes(), &discarded); err != nil {
		t.Fatal(err)
	}
	if discarded.IsBill || discarded.Stored || discarded.Reason == "" {
		t.Fatalf("%+v", discarded)
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
			ID int64 `json:"id"`
		} `json:"bills"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Count != 1 || listed.Bills[0].ID != created.Bill.ID {
		t.Fatalf("%+v", listed)
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
