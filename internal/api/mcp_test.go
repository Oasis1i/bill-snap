package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Oasis1i/bill-snap/internal/extract"
	"github.com/Oasis1i/bill-snap/internal/store"
)

func TestMCPRouteUsesItsOwnToken(t *testing.T) {
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
	srv := New(Options{
		Token:     "upload-token",
		MCPToken:  "mcp-token",
		UploadDir: filepath.Join(dir, "uploads"),
		Location:  loc,
		Store:     st,
		Extractor: extract.NewMock(),
	})
	h := srv.Handler()

	rec := mcpPost(h, "upload-token", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("upload token on mcp %d %s", rec.Code, rec.Body.String())
	}
	rec = mcpPost(h, "mcp-token", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "2025-03-26") {
		t.Fatalf("mcp token %d %s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/bills", nil)
	req.Header.Set("Authorization", "Bearer mcp-token")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("mcp token on bills %d %s", rec.Code, rec.Body.String())
	}

	closed := New(Options{
		Token:     "upload-token",
		UploadDir: filepath.Join(dir, "uploads2"),
		Location:  loc,
		Store:     st,
		Extractor: extract.NewMock(),
	}).Handler()
	rec = mcpPost(closed, "upload-token", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unset token %d %s", rec.Code, rec.Body.String())
	}

	for _, path := range []string{"/uploads/secret.png", "/api/v1/bills/1/image", "/mcp/image"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

func mcpPost(h http.Handler, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
