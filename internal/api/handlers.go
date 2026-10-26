package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Oasis1i/bill-snap/internal/model"
	"github.com/Oasis1i/bill-snap/internal/store"
)

type apiError struct {
	Error string `json:"error"`
}

type uploadResponse struct {
	IsBill      bool        `json:"is_bill"`
	Duplicate   bool        `json:"duplicate"`
	Stored      bool        `json:"stored"`
	Reason      string      `json:"reason,omitempty"`
	Notes       string      `json:"notes,omitempty"`
	ContentHash string      `json:"content_hash,omitempty"`
	Bill        *model.Bill `json:"bill,omitempty"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"version": s.version,
	})
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBytes+1<<20)
	if err := r.ParseMultipartForm(s.maxBytes); err != nil {
		if isTooLarge(err) {
			writeJSON(w, http.StatusRequestEntityTooLarge, apiError{Error: "image exceeds upload limit"})
			return
		}
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid multipart form"})
		return
	}
	f, _, err := r.FormFile("image")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: `missing form field "image"`})
		return
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, s.maxBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "could not read image"})
		return
	}
	if int64(len(data)) > s.maxBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, apiError{Error: "image exceeds upload limit"})
		return
	}
	if len(data) == 0 {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "empty image"})
		return
	}
	mime, ext, ok := sniffImage(data)
	if !ok {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "image must be png, jpeg, or webp"})
		return
	}

	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	res, err := s.extract.Extract(r.Context(), data, mime)
	if err != nil {
		slog.Error("extract", "err", err)
		writeJSON(w, http.StatusBadGateway, apiError{Error: "vision extractor failed: " + err.Error()})
		return
	}
	if !res.IsBill {
		reason := res.Notes
		if reason == "" {
			reason = "not a bill"
		}
		slog.Info("upload discarded", "reason", reason)
		writeJSON(w, http.StatusOK, uploadResponse{
			IsBill:      false,
			Duplicate:   false,
			Stored:      false,
			Reason:      reason,
			Notes:       res.Notes,
			ContentHash: hash,
		})
		return
	}
	if res.PaidAt.IsZero() {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "extractor returned a bill without paid_at"})
		return
	}
	if res.AmountCents < 0 {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: "extractor returned a negative amount"})
		return
	}
	in := store.NewBill{
		AmountCents:     res.AmountCents,
		Currency:        res.Currency,
		Merchant:        res.Merchant,
		Category:        res.Category,
		PaidAt:          res.PaidAt,
		PaidAtDefaulted: res.PaidAtDefaulted,
		OrderNo:         res.OrderNo,
		Note:            res.Notes,
		ContentHash:     hash,
		RawModelJSON:    string(res.RawJSON),
	}
	if existing, found, err := s.store.FindDuplicate(r.Context(), in); err != nil {
		slog.Error("lookup", "err", err)
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "could not read bills"})
		return
	} else if found {
		writeJSON(w, http.StatusOK, uploadResponse{IsBill: true, Duplicate: true, Stored: false, Bill: &existing})
		return
	}

	path, err := saveImage(s.uploadDir, hash, ext, data)
	if err != nil {
		slog.Error("save image", "err", err)
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "could not save image"})
		return
	}
	in.ImagePath = path
	bill, dup, err := s.store.InsertBill(r.Context(), in)
	if err != nil {
		if _, found, ferr := s.store.FindByContentHash(r.Context(), hash); ferr == nil && !found {
			_ = os.Remove(path)
		}
		slog.Error("insert", "err", err)
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "could not store bill"})
		return
	}
	if dup {
		if bill.ImagePath != path {
			_ = os.Remove(path)
		}
		writeJSON(w, http.StatusOK, uploadResponse{IsBill: true, Duplicate: true, Stored: false, Bill: &bill})
		return
	}
	slog.Info("bill stored", "id", bill.ID, "category", bill.Category)
	writeJSON(w, http.StatusCreated, uploadResponse{IsBill: true, Duplicate: false, Stored: true, Bill: &bill})
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	f, from, to, cat, err := s.filter(r, true)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
		return
	}
	bills, err := s.store.List(r.Context(), f)
	if err != nil {
		slog.Error("list", "err", err)
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "could not list bills"})
		return
	}
	writeJSON(w, http.StatusOK, model.BillList{
		From:     from,
		To:       to,
		Category: cat,
		Count:    len(bills),
		Bills:    bills,
	})
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	f, from, to, cat, err := s.filter(r, false)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
		return
	}
	sum, err := s.store.Summarize(r.Context(), f)
	if err != nil {
		slog.Error("summary", "err", err)
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "could not summarize bills"})
		return
	}
	sum.From = from
	sum.To = to
	sum.Category = cat
	writeJSON(w, http.StatusOK, sum)
}

func (s *Server) filter(r *http.Request, withLimit bool) (store.ListFilter, string, string, string, error) {
	q := r.URL.Query()
	from := strings.TrimSpace(q.Get("from"))
	to := strings.TrimSpace(q.Get("to"))
	cat := strings.TrimSpace(q.Get("category"))
	limit := 0
	limitSet := false
	if withLimit {
		raw := strings.TrimSpace(q.Get("limit"))
		if raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				return store.ListFilter{}, "", "", "", errors.New("limit must be an integer")
			}
			limit = n
			limitSet = true
		}
	}
	start, end, category, lim, err := model.ResolveQuery(from, to, cat, limit, limitSet, s.loc)
	if err != nil {
		return store.ListFilter{}, "", "", "", err
	}
	return store.ListFilter{From: start, To: end, Category: category, Limit: lim}, from, to, category, nil
}

func saveImage(dir, hash, ext string, data []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, hash+ext)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return path, nil
}

func sniffImage(data []byte) (mime, ext string, ok bool) {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return "image/png", ".png", true
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg", ".jpg", true
	case len(data) >= 12 && bytes.Equal(data[0:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp", ".webp", true
	default:
		return "", "", false
	}
}

func isTooLarge(err error) bool {
	var maxBytes *http.MaxBytesError
	if errors.As(err, &maxBytes) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "too large")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
