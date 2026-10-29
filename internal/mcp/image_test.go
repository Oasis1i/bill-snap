package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Oasis1i/bill-snap/internal/store"
	"github.com/Oasis1i/bill-snap/internal/testimg"
)

func TestResolveBillImage(t *testing.T) {
	dir := t.TempDir()
	upload := filepath.Join(dir, "uploads")
	if err := os.MkdirAll(upload, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "secret.png")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(upload, "ok.png")
	if err := os.WriteFile(inside, []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := resolveBillImage(upload, inside)
	if err != nil {
		t.Fatal(err)
	}
	wantInside, err := filepath.EvalSymlinks(inside)
	if err != nil {
		t.Fatal(err)
	}
	if got != wantInside {
		t.Fatalf("inside %s want %s", got, wantInside)
	}

	// ".." is cleaned, then rejected because the result leaves the directory.
	// A prefix check on the raw string would have allowed this.
	if _, err := resolveBillImage(upload, upload+"/../secret.png"); !errors.Is(err, errEscape) {
		t.Fatalf("lexical escape %v", err)
	}
	if _, err := resolveBillImage(upload, outside); !errors.Is(err, errEscape) {
		t.Fatalf("absolute escape %v", err)
	}

	sibling := upload + "-evil"
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	siblingFile := filepath.Join(sibling, "a.png")
	if err := os.WriteFile(siblingFile, []byte("sib"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveBillImage(upload, siblingFile); !errors.Is(err, errEscape) {
		t.Fatalf("sibling prefix %v", err)
	}

	link := filepath.Join(upload, "link.png")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveBillImage(upload, link); !errors.Is(err, errEscape) {
		t.Fatalf("symlink escape %v", err)
	}

	linkDir := filepath.Join(upload, "out")
	if err := os.Symlink(dir, linkDir); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveBillImage(upload, filepath.Join(linkDir, "secret.png")); !errors.Is(err, errEscape) {
		t.Fatalf("symlink dir escape %v", err)
	}
	if _, err := resolveBillImage(upload, filepath.Join(linkDir, "missing.png")); !errors.Is(err, errEscape) {
		t.Fatalf("missing through symlink dir %v", err)
	}

	if _, err := resolveBillImage(upload, filepath.Join(upload, "gone.png")); !errors.Is(err, errMissing) {
		t.Fatalf("missing %v", err)
	}
	if _, err := resolveBillImage(upload, "  "); !errors.Is(err, errNoImage) {
		t.Fatalf("empty %v", err)
	}
	if _, err := resolveBillImage("", inside); !errors.Is(err, errMissing) {
		t.Fatalf("no upload dir %v", err)
	}
	if _, err := resolveBillImage(upload, upload); !errors.Is(err, errMissing) {
		t.Fatalf("directory %v", err)
	}

	// ".." that still lands inside the upload directory is allowed.
	nested := upload + "/sub/../ok.png"
	got, err = resolveBillImage(upload, nested)
	if err != nil {
		t.Fatal(err)
	}
	if got != wantInside {
		t.Fatalf("cleaned inside %s", got)
	}

	real := filepath.Join(dir, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	via := filepath.Join(dir, "via")
	if err := os.Symlink(real, via); err != nil {
		t.Fatal(err)
	}
	realFile := filepath.Join(real, "a.png")
	if err := os.WriteFile(realFile, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = resolveBillImage(via, filepath.Join(via, "a.png"))
	if err != nil {
		t.Fatal(err)
	}
	wantReal, err := filepath.EvalSymlinks(realFile)
	if err != nil {
		t.Fatal(err)
	}
	if got != wantReal {
		t.Fatalf("upload symlink %s want %s", got, wantReal)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(cwd, inside)
	if err != nil {
		t.Fatal(err)
	}
	got, err = resolveBillImage(upload, rel)
	if err != nil {
		t.Fatalf("relative %s: %v", rel, err)
	}
	if got != wantInside {
		t.Fatalf("relative resolved %s", got)
	}
}

func TestBillImageTool(t *testing.T) {
	dir := t.TempDir()
	upload := filepath.Join(dir, "uploads")
	if err := os.MkdirAll(upload, 0o755); err != nil {
		t.Fatal(err)
	}
	smallPath := filepath.Join(upload, "small.png")
	small := writePNG(t, smallPath, 10, 20)
	largePath := filepath.Join(upload, "large.png")
	writePNG(t, largePath, 2000, 1000)
	jpegPath := filepath.Join(upload, "wide.jpg")
	writeJPEG(t, jpegPath, 1800, 600)
	outside := filepath.Join(dir, "secret.png")
	outsideBytes := writePNG(t, outside, 32, 32)
	link := filepath.Join(upload, "link.png")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	badPath := filepath.Join(upload, "note.png")
	if err := os.WriteFile(badPath, []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, loc := testStore(t)
	smallBill := mustInsert(t, st, store.NewBill{
		AmountCents: 2590,
		Currency:    "CNY",
		Merchant:    "美团外卖",
		Category:    "外卖",
		PaidAt:      time.Date(2026, 10, 29, 16, 30, 0, 0, time.UTC),
		Note:        "晚饭",
		ImagePath:   smallPath,
		ContentHash: "hash-small",
	})
	largeBill := mustInsert(t, st, store.NewBill{
		AmountCents: 3500,
		Currency:    "CNY",
		Merchant:    "盒马",
		Category:    "日用",
		PaidAt:      time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC),
		ImagePath:   largePath,
		ContentHash: "hash-large",
	})
	jpegBill := mustInsert(t, st, store.NewBill{
		AmountCents: 1000,
		Currency:    "CNY",
		Merchant:    "瑞幸",
		Category:    "餐饮",
		PaidAt:      time.Date(2026, 10, 2, 2, 0, 0, 0, time.UTC),
		ImagePath:   jpegPath,
		ContentHash: "hash-jpeg",
	})
	emptyBill := mustInsert(t, st, store.NewBill{
		AmountCents: 500,
		Currency:    "CNY",
		Merchant:    "现金",
		Category:    "其他",
		PaidAt:      time.Date(2026, 10, 4, 2, 0, 0, 0, time.UTC),
		ImagePath:   "",
		ContentHash: "hash-empty",
	})
	goneBill := mustInsert(t, st, store.NewBill{
		AmountCents: 600,
		Currency:    "CNY",
		Merchant:    "丢了",
		Category:    "其他",
		PaidAt:      time.Date(2026, 10, 5, 2, 0, 0, 0, time.UTC),
		ImagePath:   filepath.Join(upload, "gone.png"),
		ContentHash: "hash-gone",
	})
	escapeBill := mustInsert(t, st, store.NewBill{
		AmountCents: 700,
		Currency:    "CNY",
		Merchant:    "越界",
		Category:    "其他",
		PaidAt:      time.Date(2026, 10, 6, 2, 0, 0, 0, time.UTC),
		ImagePath:   outside,
		ContentHash: "hash-escape",
	})
	dotdotBill := mustInsert(t, st, store.NewBill{
		AmountCents: 800,
		Currency:    "CNY",
		Merchant:    "点点",
		Category:    "其他",
		PaidAt:      time.Date(2026, 10, 7, 2, 0, 0, 0, time.UTC),
		ImagePath:   upload + "/../secret.png",
		ContentHash: "hash-dotdot",
	})
	linkBill := mustInsert(t, st, store.NewBill{
		AmountCents: 900,
		Currency:    "CNY",
		Merchant:    "链接",
		Category:    "其他",
		PaidAt:      time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC),
		ImagePath:   link,
		ContentHash: "hash-link",
	})
	badBill := mustInsert(t, st, store.NewBill{
		AmountCents: 1100,
		Currency:    "CNY",
		Merchant:    "坏图",
		Category:    "其他",
		PaidAt:      time.Date(2026, 10, 9, 2, 0, 0, 0, time.UTC),
		ImagePath:   badPath,
		ContentHash: "hash-bad",
	})

	h := NewHandler(st, loc, "mcp-secret", upload)

	rec := postMCP(h, "", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "get_bill_image", "arguments": map[string]any{"id": smallBill.ID}},
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("auth %d %s", rec.Code, rec.Body.String())
	}

	listText := callToolHTTP(t, h, "list_bills", map[string]any{"from": "2026-10-01", "to": "2026-10-31", "limit": 50})
	if strings.Contains(listText, "secret.png") || strings.Contains(listText, "image_path") || strings.Contains(listText, upload) {
		t.Fatalf("leaked path %s", listText)
	}
	var listed struct {
		Bills []struct {
			ID       int64 `json:"id"`
			HasImage bool  `json:"has_image"`
		} `json:"bills"`
	}
	if err := unmarshalToolJSON(t, listText, &listed); err != nil {
		t.Fatal(err)
	}
	wantHas := map[int64]bool{
		smallBill.ID:  true,
		largeBill.ID:  true,
		jpegBill.ID:   true,
		emptyBill.ID:  false,
		goneBill.ID:   false,
		escapeBill.ID: false,
		dotdotBill.ID: false,
		linkBill.ID:   false,
		badBill.ID:    true,
	}
	if len(listed.Bills) != len(wantHas) {
		t.Fatalf("count %d %s", len(listed.Bills), listText)
	}
	for _, item := range listed.Bills {
		got, ok := wantHas[item.ID]
		if !ok || got != item.HasImage {
			t.Fatalf("id %d has_image %v want %v", item.ID, item.HasImage, got)
		}
	}

	assertBillImage(t, h, smallBill.ID, "2026-10-30 外卖 25.90 元", "image/png", func(w, hgt int, data []byte) {
		if w != 10 || hgt != 20 {
			t.Fatalf("upscaled %dx%d", w, hgt)
		}
		if !bytes.Equal(data, small) {
			t.Fatal("small png was re-encoded")
		}
	})
	assertBillImage(t, h, largeBill.ID, "2026-10-03 日用 35.00 元", "image/png", func(w, hgt int, data []byte) {
		if w != 1024 || hgt != 512 {
			t.Fatalf("resized %dx%d", w, hgt)
		}
		if w > 1024 || hgt > 1024 {
			t.Fatalf("side %dx%d", w, hgt)
		}
	})
	assertBillImage(t, h, jpegBill.ID, "2026-10-02 餐饮 10.00 元", "image/jpeg", func(w, hgt int, data []byte) {
		if w != 1024 || hgt != 341 {
			t.Fatalf("jpeg %dx%d", w, hgt)
		}
		if data[0] != 0xff || data[1] != 0xd8 {
			t.Fatal("jpeg magic")
		}
	})

	// A client-supplied path is ignored. Only the stored id is read.
	result := callToolRaw(t, h, "get_bill_image", map[string]any{"id": smallBill.ID, "path": outside})
	if result["isError"] == true {
		t.Fatalf("extra path %v", result)
	}
	rawBody := result["content"].([]any)[0].(map[string]any)["data"].(string)
	decoded, err := base64.StdEncoding.DecodeString(rawBody)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, small) || bytes.Equal(decoded, outsideBytes) {
		t.Fatal("client path was honored")
	}

	assertImageError(t, h, map[string]any{"id": int64(999999)}, msgBillMissing)
	assertImageError(t, h, map[string]any{"id": emptyBill.ID}, msgNoImage)
	assertImageError(t, h, map[string]any{"id": goneBill.ID}, msgImageGone)
	assertImageError(t, h, map[string]any{"id": escapeBill.ID}, msgImageGone)
	assertImageError(t, h, map[string]any{"id": dotdotBill.ID}, msgImageGone)
	assertImageError(t, h, map[string]any{"id": linkBill.ID}, msgImageGone)
	assertImageError(t, h, map[string]any{"id": badBill.ID}, msgImageBad)
	for _, args := range []map[string]any{
		{},
		{"id": nil},
		{"id": 0},
		{"id": -3},
		{"id": 1.5},
		{"id": "12"},
		{"id": true},
	} {
		assertImageError(t, h, args, msgBadID)
	}

	outsideB64 := base64.StdEncoding.EncodeToString(outsideBytes)
	for _, id := range []int64{escapeBill.ID, dotdotBill.ID, linkBill.ID} {
		rec = postMCP(h, "mcp-secret", map[string]any{
			"jsonrpc": "2.0", "id": 3, "method": "tools/call",
			"params": map[string]any{"name": "get_bill_image", "arguments": map[string]any{"id": id}},
		})
		body := rec.Body.String()
		if strings.Contains(body, outsideB64) || strings.Contains(body, "secret.png") || strings.Contains(body, `"type":"image"`) {
			t.Fatalf("traversal id %d leaked %s", id, body)
		}
	}

	listedTools := postMCP(h, "mcp-secret", map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	msg := decodeRPC(t, listedTools.Body.String())
	var imageTool map[string]any
	for _, tool := range msg["result"].(map[string]any)["tools"].([]any) {
		item := tool.(map[string]any)
		if item["name"] == "get_bill_image" {
			imageTool = item
		}
	}
	if imageTool == nil {
		t.Fatal("missing tool")
	}
	if imageTool["description"] != "返回 list_bills 查到的某一笔账单的原始付款截图。id 是该账单编号。图片最长边不超过 1024 像素且不会放大，并附带日期、分类和金额" {
		t.Fatal(imageTool["description"])
	}
	schema := imageTool["inputSchema"].(map[string]any)
	reqd := schema["required"].([]any)
	if len(reqd) != 1 || reqd[0] != "id" {
		t.Fatal(reqd)
	}
	idProp := schema["properties"].(map[string]any)["id"].(map[string]any)
	if idProp["type"] != "integer" || idProp["minimum"].(float64) != 1 {
		t.Fatal(idProp)
	}
}

func TestFitSide(t *testing.T) {
	w, h, resized := fitSide(10, 20, 1024)
	if resized || w != 10 || h != 20 {
		t.Fatalf("noscale %d %d %v", w, h, resized)
	}
	w, h, resized = fitSide(2000, 1000, 1024)
	if !resized || w != 1024 || h != 512 {
		t.Fatalf("wide %d %d %v", w, h, resized)
	}
	w, h, resized = fitSide(1800, 600, 1024)
	if !resized || w != 1024 || h != 341 {
		t.Fatalf("jpeg ratio %d %d", w, h)
	}
	w, h, resized = fitSide(1024, 100, 1024)
	if resized || w != 1024 || h != 100 {
		t.Fatalf("exact %d %d %v", w, h, resized)
	}
}

func mustInsert(t *testing.T, st *store.Store, in store.NewBill) storeBill {
	t.Helper()
	b, dup, err := st.InsertBill(context.Background(), in)
	if err != nil || dup {
		t.Fatalf("insert dup=%v err=%v", dup, err)
	}
	return storeBill{ID: b.ID}
}

type storeBill struct {
	ID int64
}

func writePNG(t *testing.T, path string, w, h int) []byte {
	t.Helper()
	b, err := testimg.NoisyPNG(w, h, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return b
}

func writeJPEG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 80, A: 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
}

func unmarshalToolJSON(t *testing.T, text string, dest any) error {
	t.Helper()
	return json.Unmarshal([]byte(text), dest)
}

func callToolRaw(t *testing.T, h http.Handler, name string, args map[string]any) map[string]any {
	t.Helper()
	rec := postMCP(h, "mcp-secret", map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": args},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	msg := decodeRPC(t, rec.Body.String())
	if msg["error"] != nil {
		t.Fatalf("rpc error %v", msg["error"])
	}
	return msg["result"].(map[string]any)
}

func assertBillImage(t *testing.T, h http.Handler, id int64, wantText, wantMime string, check func(w, h int, data []byte)) {
	t.Helper()
	result := callToolRaw(t, h, "get_bill_image", map[string]any{"id": id})
	if result["isError"] == true {
		t.Fatalf("error %v", result)
	}
	content := result["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content %v", content)
	}
	img := content[0].(map[string]any)
	text := content[1].(map[string]any)
	if img["type"] != "image" || img["mimeType"] != wantMime {
		t.Fatalf("image %v", img["mimeType"])
	}
	if text["type"] != "text" || text["text"] != wantText {
		t.Fatalf("text %v", text["text"])
	}
	data, err := base64.StdEncoding.DecodeString(img["data"].(string))
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width > 1024 || cfg.Height > 1024 {
		t.Fatalf("bounds %dx%d", cfg.Width, cfg.Height)
	}
	switch wantMime {
	case "image/png":
		if format != "png" || !bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G'}) {
			t.Fatalf("png format %s", format)
		}
	case "image/jpeg":
		if format != "jpeg" {
			t.Fatalf("jpeg format %s", format)
		}
	}
	if strings.Contains(wantText, "image_path") || strings.Contains(text["text"].(string), "secret") {
		t.Fatal(text)
	}
	check(cfg.Width, cfg.Height, data)
}

func assertImageError(t *testing.T, h http.Handler, args map[string]any, want string) {
	t.Helper()
	result := callToolRaw(t, h, "get_bill_image", args)
	if result["isError"] != true {
		t.Fatalf("expected error for %v: %v", args, result)
	}
	content := result["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content %v", content)
	}
	item := content[0].(map[string]any)
	if item["type"] != "text" || item["text"] != want {
		t.Fatalf("got %#v want %s", item, want)
	}
	if item["data"] != nil || item["mimeType"] != nil {
		t.Fatalf("image fields on error %v", item)
	}
	assertPhrase(t, want)
}
