package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "bills.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestRootSchemaMatchesEmbed(t *testing.T) {
	root, err := os.ReadFile(filepath.Join("..", "..", "schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(root)) != strings.TrimSpace(schemaSQL) {
		t.Fatal("schema.sql drifted from internal/store/schema.sql")
	}
}

func TestInsertDedupListSummary(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	paid := time.Date(2026, 10, 26, 12, 0, 40, 0, time.UTC)

	b1, dup, err := st.InsertBill(ctx, NewBill{
		AmountCents:  1850,
		Currency:     "cny",
		Merchant:     " 美团外卖 ",
		Category:     "外卖",
		PaidAt:       paid,
		Note:         "first",
		ImagePath:    "data/uploads/a.png",
		ContentHash:  strings.Repeat("a", 64),
		RawModelJSON: `{"provider":"test"}`,
	})
	if err != nil || dup {
		t.Fatalf("insert: dup=%v err=%v", dup, err)
	}
	if b1.Currency != "CNY" || b1.Merchant != "美团外卖" || b1.PaidAt != "2026-10-26T12:00:00Z" || b1.Amount != "18.50" {
		t.Fatalf("%+v", b1)
	}

	b2, dup, err := st.InsertBill(ctx, NewBill{
		AmountCents: 999,
		Currency:    "CNY",
		Merchant:    "别的店",
		Category:    "餐饮",
		PaidAt:      paid.Add(time.Hour),
		ContentHash: strings.Repeat("a", 64),
		ImagePath:   "data/uploads/a.png",
	})
	if err != nil || !dup || b2.ID != b1.ID {
		t.Fatalf("same bytes should return the first row: %+v dup=%v err=%v", b2, dup, err)
	}

	b3, dup, err := st.InsertBill(ctx, NewBill{
		AmountCents: 1850,
		Currency:    "CNY",
		Merchant:    "美团外卖",
		Category:    "外卖",
		PaidAt:      paid.Add(10 * time.Second),
		ContentHash: strings.Repeat("b", 64),
		ImagePath:   "data/uploads/b.png",
	})
	if err != nil || !dup || b3.ID != b1.ID || b3.Note != "first" {
		t.Fatalf("same payment should be idempotent: %+v dup=%v err=%v", b3, dup, err)
	}

	b4, dup, err := st.InsertBill(ctx, NewBill{
		AmountCents: 500,
		Currency:    "CNY",
		Merchant:    "滴滴出行",
		Category:    "交通",
		PaidAt:      time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC),
		ContentHash: strings.Repeat("c", 64),
	})
	if err != nil || dup {
		t.Fatalf("different payment: %+v dup=%v err=%v", b4, dup, err)
	}

	from := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	list, err := st.List(ctx, ListFilter{From: &from, To: &to, Category: "外卖"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != b1.ID {
		t.Fatalf("%+v", list)
	}

	sum, err := st.Summarize(ctx, ListFilter{From: &from, To: &to})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Count != 1 || len(sum.Totals) != 1 || sum.Totals[0].AmountCents != 1850 {
		t.Fatalf("%+v", sum)
	}
	empty, err := st.Summarize(ctx, ListFilter{Category: "不存在"})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Count != 0 || empty.Totals == nil || len(empty.Totals) != 0 || empty.ByCategory == nil {
		t.Fatalf("%+v", empty)
	}

	if _, _, err := st.InsertBill(ctx, NewBill{AmountCents: 1, PaidAt: paid, ContentHash: ""}); err == nil {
		t.Fatal("expected missing hash error")
	}
}
