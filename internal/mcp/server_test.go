package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Oasis1i/bill-snap/internal/model"
	"github.com/Oasis1i/bill-snap/internal/store"
)

func TestServeListAndSummary(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bills.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, _, err := st.InsertBill(context.Background(), store.NewBill{
		AmountCents:  3500,
		Currency:     "CNY",
		Merchant:     "美团外卖",
		Category:     "外卖",
		PaidAt:       time.Date(2026, 10, 26, 12, 30, 0, 0, time.UTC),
		Note:         "午饭",
		ImagePath:    "data/uploads/secret.png",
		ContentHash:  strings.Repeat("d", 64),
		RawModelJSON: `{"model":"secret-model"}`,
	}); err != nil {
		t.Fatal(err)
	}

	var in bytes.Buffer
	msgs := []any{
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "0"},
		}},
		map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
		map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{
			"name":      "summarize_spend",
			"arguments": map[string]any{"from": "2026-10-01", "to": "2026-10-31"},
		}},
		map[string]any{"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": map[string]any{
			"name":      "list_bills",
			"arguments": map[string]any{"from": "2026-10-01", "to": "2026-10-31"},
		}},
		map[string]any{"jsonrpc": "2.0", "id": 5, "method": "tools/call", "params": map[string]any{
			"name":      "summarize_spend",
			"arguments": map[string]any{"from": "not-a-date", "to": "2026-10-31"},
		}},
		map[string]any{"jsonrpc": "2.0", "id": 6, "method": "nope"},
		map[string]any{"jsonrpc": "2.0", "id": 7, "method": "tools/call", "params": map[string]any{
			"name": "list_categories",
		}},
	}
	for _, m := range msgs {
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		in.Write(b)
		in.WriteByte('\n')
	}

	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Serve(&in, &out, st, loc, ""); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 7 {
		t.Fatalf("lines=%d\n%s", len(lines), out.String())
	}
	byID := map[float64]map[string]any{}
	for _, line := range lines {
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatal(err)
		}
		byID[msg["id"].(float64)] = msg
	}
	init := byID[1]["result"].(map[string]any)
	if init["protocolVersion"] != "2024-11-05" {
		t.Fatal(init)
	}
	tools := byID[2]["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.(map[string]any)["name"].(string)] = true
	}
	for _, name := range []string{"list_bills", "summarize_spend", "list_categories", "get_bill_image"} {
		if !names[name] {
			t.Fatalf("missing %s in %v", name, names)
		}
	}
	if len(tools) != 4 {
		t.Fatal(tools)
	}
	var listed map[string]any
	for _, tool := range tools {
		item := tool.(map[string]any)
		if item["name"] == "list_bills" {
			listed = item
		}
	}
	schema := listed["inputSchema"].(map[string]any)
	props := schema["properties"].(map[string]any)
	enum := props["category"].(map[string]any)["enum"].([]any)
	if len(enum) != len(model.Categories()) {
		t.Fatalf("category enum %v", enum)
	}
	for i, c := range model.Categories() {
		if enum[i] != c {
			t.Fatalf("enum[%d]=%v want %s", i, enum[i], c)
		}
	}
	sumText := byID[3]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	var sum struct {
		Amount string `json:"amount"`
		Count  int    `json:"count"`
		Groups []struct {
			Category string `json:"category"`
			Amount   string `json:"amount"`
			Count    int    `json:"count"`
		} `json:"groups"`
	}
	if err := json.Unmarshal([]byte(sumText), &sum); err != nil {
		t.Fatal(err)
	}
	if sum.Count != 1 || sum.Amount != "35.00" || len(sum.Groups) != 1 || sum.Groups[0].Category != "外卖" {
		t.Fatalf("%s", sumText)
	}
	listText := byID[4]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if strings.Contains(listText, "image_path") || strings.Contains(listText, "secret.png") || strings.Contains(listText, "secret-model") || strings.Contains(listText, "raw_model") {
		t.Fatalf("leaked %s", listText)
	}
	var listedBills struct {
		Count int `json:"count"`
		Bills []struct {
			Amount   string `json:"amount"`
			PaidAt   string `json:"paid_at"`
			Category string `json:"category"`
			Merchant string `json:"merchant"`
			Note     string `json:"note"`
		} `json:"bills"`
	}
	if err := json.Unmarshal([]byte(listText), &listedBills); err != nil {
		t.Fatal(err)
	}
	if listedBills.Count != 1 || listedBills.Bills[0].Amount != "35.00" || listedBills.Bills[0].PaidAt != "2026-10-26 20:30" || listedBills.Bills[0].Note != "午饭" {
		t.Fatalf("%s", listText)
	}
	var rawList struct {
		Bills []map[string]any `json:"bills"`
	}
	if err := json.Unmarshal([]byte(listText), &rawList); err != nil {
		t.Fatal(err)
	}
	if rawList.Bills[0]["has_image"] != false {
		t.Fatalf("has_image %v", rawList.Bills[0]["has_image"])
	}
	bad := byID[5]["result"].(map[string]any)
	if bad["isError"] != true {
		t.Fatal(byID[5])
	}
	badText := bad["content"].([]any)[0].(map[string]any)["text"].(string)
	if badText != msgBadDate {
		t.Fatalf("bad date text %q", badText)
	}
	if byID[6]["error"] == nil {
		t.Fatal(byID[6])
	}
	catText := byID[7]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	var cats struct {
		Categories []string `json:"categories"`
	}
	if err := json.Unmarshal([]byte(catText), &cats); err != nil {
		t.Fatal(err)
	}
	if strings.Join(cats.Categories, ",") != strings.Join(model.Categories(), ",") {
		t.Fatalf("categories %v", cats.Categories)
	}
}

func TestToolEnumFollowsConfiguredCategories(t *testing.T) {
	model.UseCategories([]string{"宠物"})
	t.Cleanup(func() { model.UseCategories(model.DefaultCategories()) })

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bills.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var in bytes.Buffer
	for _, m := range []any{
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "0"},
		}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
	} {
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		in.Write(b)
		in.WriteByte('\n')
	}
	var out bytes.Buffer
	if err := Serve(&in, &out, st, time.UTC, ""); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines=%d\n%s", len(lines), out.String())
	}
	var listed map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &listed); err != nil {
		t.Fatal(err)
	}
	tools := listed["result"].(map[string]any)["tools"].([]any)
	var enum []any
	for _, tool := range tools {
		item := tool.(map[string]any)
		if item["name"] != "list_bills" {
			continue
		}
		schema := item["inputSchema"].(map[string]any)
		props := schema["properties"].(map[string]any)
		enum = props["category"].(map[string]any)["enum"].([]any)
	}
	if len(enum) != 2 || enum[0] != "宠物" || enum[1] != "其他" {
		t.Fatalf("enum %v", enum)
	}
}
