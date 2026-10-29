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
		AmountCents: 3500,
		Currency:    "CNY",
		Merchant:    "美团外卖",
		Category:    "外卖",
		PaidAt:      time.Date(2026, 10, 26, 12, 30, 0, 0, time.UTC),
		ContentHash: strings.Repeat("d", 64),
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
			"arguments": map[string]any{"from": "2026-10-01", "to": "2026-10-31", "category": "外卖"},
		}},
		map[string]any{"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": map[string]any{
			"name":      "list_bills",
			"arguments": map[string]any{"from": "2026-10-01", "to": "2026-10-31"},
		}},
		map[string]any{"jsonrpc": "2.0", "id": 5, "method": "tools/call", "params": map[string]any{
			"name":      "summarize_spend",
			"arguments": map[string]any{"from": "not-a-date"},
		}},
		map[string]any{"jsonrpc": "2.0", "id": 6, "method": "nope"},
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
	if err := Serve(&in, &out, st, loc); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 6 {
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
	if len(tools) != 2 {
		t.Fatal(tools)
	}
	for _, tool := range tools {
		schema := tool.(map[string]any)["inputSchema"].(map[string]any)
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
	}
	sumText := byID[3]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	var sum struct {
		Count  int `json:"count"`
		Totals []struct {
			AmountCents int64 `json:"amount_cents"`
		} `json:"totals"`
	}
	if err := json.Unmarshal([]byte(sumText), &sum); err != nil {
		t.Fatal(err)
	}
	if sum.Count != 1 || sum.Totals[0].AmountCents != 3500 {
		t.Fatalf("%s", sumText)
	}
	if byID[5]["result"].(map[string]any)["isError"] != true {
		t.Fatal(byID[5])
	}
	if byID[6]["error"] == nil {
		t.Fatal(byID[6])
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
	if err := Serve(&in, &out, st, time.UTC); err != nil {
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
	for _, tool := range tools {
		schema := tool.(map[string]any)["inputSchema"].(map[string]any)
		props := schema["properties"].(map[string]any)
		enum := props["category"].(map[string]any)["enum"].([]any)
		if len(enum) != 2 || enum[0] != "宠物" || enum[1] != "其他" {
			t.Fatalf("enum %v", enum)
		}
	}
}
