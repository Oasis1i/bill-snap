package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Oasis1i/bill-snap/internal/model"
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

func TestInsertNormalizesCategory(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	paid := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		in, want string
		hash     string
		cents    int64
	}{
		{" 房租 ", "房租", strings.Repeat("e", 64), 100},
		{"外卖配送", "其他", strings.Repeat("f", 64), 200},
		{"", "其他", strings.Repeat("1", 64), 300},
		{"外 卖", "其他", strings.Repeat("2", 64), 400},
		{"waimai", "其他", strings.Repeat("3", 64), 500},
	}
	for _, tc := range cases {
		b, dup, err := st.InsertBill(ctx, NewBill{
			AmountCents: tc.cents,
			Currency:    "CNY",
			Merchant:    "店" + tc.hash[:4],
			Category:    tc.in,
			PaidAt:      paid,
			ContentHash: tc.hash,
		})
		if err != nil || dup {
			t.Fatalf("%q: %+v dup=%v err=%v", tc.in, b, dup, err)
		}
		if b.Category != tc.want {
			t.Fatalf("%q: got %q want %q", tc.in, b.Category, tc.want)
		}
	}
	other, err := st.List(ctx, ListFilter{Category: "其他"})
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 4 {
		t.Fatalf("其他: %+v", other)
	}
	rent, err := st.List(ctx, ListFilter{Category: "房租"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rent) != 1 || rent[0].Category != "房租" {
		t.Fatalf("房租: %+v", rent)
	}
	// A close variant is not stored as 外卖, and a query for 外卖 does not match it.
	waimai, err := st.List(ctx, ListFilter{Category: "外卖"})
	if err != nil {
		t.Fatal(err)
	}
	if len(waimai) != 0 {
		t.Fatalf("外卖: %+v", waimai)
	}
	// Unknown query text stays an exact filter. It is not rewritten to 其他.
	miss, err := st.List(ctx, ListFilter{Category: "不存在"})
	if err != nil {
		t.Fatal(err)
	}
	if len(miss) != 0 {
		t.Fatalf("不存在: %+v", miss)
	}
}

func TestInsertUsesConfiguredCategories(t *testing.T) {
	model.UseCategories([]string{"宠物"})
	t.Cleanup(func() { model.UseCategories(model.DefaultCategories()) })
	st := openTest(t)
	ctx := context.Background()
	paid := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)

	kept, dup, err := st.InsertBill(ctx, NewBill{
		AmountCents: 100,
		Currency:    "CNY",
		Merchant:    "宠物店",
		Category:    " 宠物 ",
		PaidAt:      paid,
		ContentHash: strings.Repeat("4", 64),
	})
	if err != nil || dup || kept.Category != "宠物" {
		t.Fatalf("%+v dup=%v err=%v", kept, dup, err)
	}
	dropped, dup, err := st.InsertBill(ctx, NewBill{
		AmountCents: 200,
		Currency:    "CNY",
		Merchant:    "美团外卖",
		Category:    "外卖",
		PaidAt:      paid,
		ContentHash: strings.Repeat("5", 64),
	})
	if err != nil || dup || dropped.Category != "其他" {
		t.Fatalf("%+v dup=%v err=%v", dropped, dup, err)
	}
}

func TestOpenDefaultsToShanghai(t *testing.T) {
	st := openTest(t)
	if st.loc == nil || st.loc.String() != "Asia/Shanghai" {
		t.Fatalf("location %v", st.loc)
	}
}

func TestOrderNumberDedup(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	loc := shanghai(t)
	st.SetLocation(loc)

	first, dup, err := st.InsertBill(ctx, NewBill{
		AmountCents: 1070,
		Currency:    "CNY",
		Merchant:    "沙县小吃",
		Category:    "餐饮",
		PaidAt:      time.Date(2026, 10, 26, 12, 9, 0, 0, loc),
		OrderNo:     "  WX-4200 123 ",
		ContentHash: strings.Repeat("a", 64),
		Note:        "first",
	})
	if err != nil || dup {
		t.Fatalf("insert: dup=%v err=%v", dup, err)
	}
	if first.OrderNo != "WX4200123" || first.PaidAtDefaulted {
		t.Fatalf("%+v", first)
	}

	// Same order number, different amount, merchant, and day.
	again, dup, err := st.InsertBill(ctx, NewBill{
		AmountCents: 2000,
		Currency:    "cny",
		Merchant:    "沙县小吃(路店)",
		Category:    "餐饮",
		PaidAt:      time.Date(2026, 10, 27, 8, 0, 0, 0, loc),
		OrderNo:     "wx4200123",
		ContentHash: strings.Repeat("b", 64),
	})
	if err != nil || !dup || again.ID != first.ID || again.Note != "first" {
		t.Fatalf("order number should win: %+v dup=%v err=%v", again, dup, err)
	}

	// Same minute as the first row, but a different order number. The plain
	// dedup_key would collide; the second order must still be stored.
	other, dup, err := st.InsertBill(ctx, NewBill{
		AmountCents: 1070,
		Currency:    "CNY",
		Merchant:    "沙县小吃",
		PaidAt:      time.Date(2026, 10, 26, 12, 9, 40, 0, loc),
		OrderNo:     "ORD-B",
		ContentHash: strings.Repeat("c", 64),
	})
	if err != nil || dup || other.ID == first.ID {
		t.Fatalf("different order numbers in the same minute: %+v dup=%v err=%v", other, dup, err)
	}
	wantKey := model.MakeDistinctOrderDedupKey(1070, "CNY", "沙县小吃", otherPaid(loc), "ORD-B")
	if other.DedupKey != wantKey || other.OrderNo != "ORDB" {
		t.Fatalf("key %s order %s", other.DedupKey, other.OrderNo)
	}
	if first.DedupKey != model.MakeDedupKey(1070, "CNY", "沙县小吃", time.Date(2026, 10, 26, 12, 9, 0, 0, loc)) {
		t.Fatal("first row should keep the plain dedup key")
	}
}

func otherPaid(loc *time.Location) time.Time {
	return time.Date(2026, 10, 26, 12, 9, 40, 0, loc)
}

func TestDefaultedPaidAtSameLocalDay(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	loc := shanghai(t)
	st.SetLocation(loc)

	uploaded := time.Date(2026, 10, 26, 10, 15, 0, 0, loc)
	real := time.Date(2026, 10, 26, 12, 9, 0, 0, loc)
	first, dup, err := st.InsertBill(ctx, NewBill{
		AmountCents:     1070,
		Currency:        "CNY",
		Merchant:        "沙县小吃",
		Category:        "餐饮",
		PaidAt:          uploaded,
		PaidAtDefaulted: true,
		Note:            model.PaidAtDefaultedNote,
		ContentHash:     strings.Repeat("a", 64),
	})
	if err != nil || dup || !first.PaidAtDefaulted {
		t.Fatalf("defaulted insert: %+v dup=%v err=%v", first, dup, err)
	}

	second, dup, err := st.InsertBill(ctx, NewBill{
		AmountCents: 1070,
		Currency:    "CNY",
		Merchant:    " 沙县小吃 ",
		Category:    "餐饮",
		PaidAt:      real,
		ContentHash: strings.Repeat("b", 64),
	})
	if err != nil || !dup || second.ID != first.ID {
		t.Fatalf("same Shanghai day should dedup: %+v dup=%v err=%v", second, dup, err)
	}

	// Real time first, defaulted upload later, still the same local day
	// even though the UTC dates differ (00:30 and 23:00 in Shanghai).
	early := time.Date(2026, 10, 26, 0, 30, 0, 0, loc)
	late := time.Date(2026, 10, 26, 23, 0, 0, 0, loc)
	morning, dup, err := st.InsertBill(ctx, NewBill{
		AmountCents: 1800,
		Currency:    "CNY",
		Merchant:    "瑞幸咖啡",
		PaidAt:      early,
		ContentHash: strings.Repeat("c", 64),
	})
	if err != nil || dup {
		t.Fatalf("%+v dup=%v err=%v", morning, dup, err)
	}
	evening, dup, err := st.InsertBill(ctx, NewBill{
		AmountCents:     1800,
		Currency:        "CNY",
		Merchant:        "瑞幸咖啡",
		PaidAt:          late,
		PaidAtDefaulted: true,
		ContentHash:     strings.Repeat("d", 64),
	})
	if err != nil || !dup || evening.ID != morning.ID {
		t.Fatalf("shanghai day across UTC dates: %+v dup=%v err=%v", evening, dup, err)
	}

	// 23:30 and 00:30 the next calendar day in Shanghai share a UTC date.
	night, dup, err := st.InsertBill(ctx, NewBill{
		AmountCents: 2500,
		Currency:    "CNY",
		Merchant:    "星巴克",
		PaidAt:      time.Date(2026, 10, 26, 23, 30, 0, 0, loc),
		ContentHash: strings.Repeat("e", 64),
	})
	if err != nil || dup {
		t.Fatalf("%+v dup=%v err=%v", night, dup, err)
	}
	afterMidnight, dup, err := st.InsertBill(ctx, NewBill{
		AmountCents:     2500,
		Currency:        "CNY",
		Merchant:        "星巴克",
		PaidAt:          time.Date(2026, 10, 27, 0, 30, 0, 0, loc),
		PaidAtDefaulted: true,
		ContentHash:     strings.Repeat("f", 64),
	})
	if err != nil || dup || afterMidnight.ID == night.ID {
		t.Fatalf("next Shanghai day must stay separate: %+v dup=%v err=%v", afterMidnight, dup, err)
	}

	// Two real timestamps on the same day are not merged just because the
	// clock differs. dedup_key still requires the same minute.
	lunch, dup, err := st.InsertBill(ctx, NewBill{
		AmountCents: 900,
		Currency:    "CNY",
		Merchant:    "盒马鲜生",
		PaidAt:      time.Date(2026, 10, 26, 12, 0, 0, 0, loc),
		ContentHash: strings.Repeat("1", 64),
	})
	if err != nil || dup {
		t.Fatalf("%+v dup=%v err=%v", lunch, dup, err)
	}
	later, dup, err := st.InsertBill(ctx, NewBill{
		AmountCents: 900,
		Currency:    "CNY",
		Merchant:    "盒马鲜生",
		PaidAt:      time.Date(2026, 10, 26, 13, 0, 0, 0, loc),
		ContentHash: strings.Repeat("2", 64),
	})
	if err != nil || dup || later.ID == lunch.ID {
		t.Fatalf("real times an hour apart: %+v dup=%v err=%v", later, dup, err)
	}
}

func TestDistinctOrderNumbersNotMerged(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	loc := shanghai(t)
	st.SetLocation(loc)

	paid := time.Date(2026, 10, 26, 12, 9, 0, 0, loc)
	first, dup, err := st.InsertBill(ctx, NewBill{
		AmountCents: 1070,
		Currency:    "CNY",
		Merchant:    "沙县小吃",
		Category:    "餐饮",
		PaidAt:      paid,
		OrderNo:     "ORDER-A",
		ContentHash: strings.Repeat("a", 64),
	})
	if err != nil || dup {
		t.Fatalf("%+v dup=%v err=%v", first, dup, err)
	}

	// Same amount, merchant, and local day, and this paid_at was defaulted,
	// so the day rule would merge them if order numbers were ignored.
	second, dup, err := st.InsertBill(ctx, NewBill{
		AmountCents:     1070,
		Currency:        "CNY",
		Merchant:        "沙县小吃",
		Category:        "餐饮",
		PaidAt:          time.Date(2026, 10, 26, 18, 40, 0, 0, loc),
		PaidAtDefaulted: true,
		OrderNo:         "ORDER-B",
		ContentHash:     strings.Repeat("b", 64),
	})
	if err != nil || dup || second.ID == first.ID {
		t.Fatalf("distinct orders must not merge: %+v dup=%v err=%v", second, dup, err)
	}
	if second.OrderNo != "ORDERB" || !second.PaidAtDefaulted {
		t.Fatalf("%+v", second)
	}
}

func TestMigrateLegacyDefaultedPaidAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := `
CREATE TABLE bills (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    amount_cents    INTEGER NOT NULL,
    currency        TEXT    NOT NULL,
    merchant        TEXT    NOT NULL DEFAULT '',
    category        TEXT    NOT NULL DEFAULT '',
    paid_at         TEXT    NOT NULL,
    note            TEXT    NOT NULL DEFAULT '',
    image_path      TEXT    NOT NULL DEFAULT '',
    content_hash    TEXT    NOT NULL,
    dedup_key       TEXT    NOT NULL,
    raw_model_json  TEXT    NOT NULL DEFAULT '',
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL
);
CREATE UNIQUE INDEX idx_bills_dedup_key ON bills(dedup_key);
CREATE UNIQUE INDEX idx_bills_content_hash ON bills(content_hash);
`
	if _, err := db.Exec(legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO bills (
		amount_cents, currency, merchant, category, paid_at, note, image_path,
		content_hash, dedup_key, raw_model_json, created_at, updated_at
	) VALUES (1070, 'CNY', '沙县小吃', '餐饮', '2026-10-26T02:15:00Z', ?, '', ?, ?, '', '2026-10-26T02:16:00Z', '2026-10-26T02:16:00Z')`,
		model.PaidAtDefaultedNote, strings.Repeat("a", 64), strings.Repeat("d", 64),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO bills (
		amount_cents, currency, merchant, category, paid_at, note, image_path,
		content_hash, dedup_key, raw_model_json, created_at, updated_at
	) VALUES (1500, 'CNY', '瑞幸咖啡', '餐饮', '2026-10-26T01:00:00Z', 'ok', '', ?, ?, '', '2026-10-26T01:01:00Z', '2026-10-26T01:01:00Z')`,
		strings.Repeat("b", 64), strings.Repeat("e", 64),
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if st != nil {
			_ = st.Close()
		}
	})

	var indexName string
	if err := st.db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_bills_order_no'`).Scan(&indexName); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	shaxian, ok, err := st.FindByContentHash(ctx, strings.Repeat("a", 64))
	if err != nil || !ok {
		t.Fatalf("legacy row: ok=%v err=%v", ok, err)
	}
	if !shaxian.PaidAtDefaulted || shaxian.OrderNo != "" || shaxian.AmountCents != 1070 {
		t.Fatalf("backfill: %+v", shaxian)
	}
	luckin, ok, err := st.FindByContentHash(ctx, strings.Repeat("b", 64))
	if err != nil || !ok || luckin.PaidAtDefaulted {
		t.Fatalf("note without the defaulted sentence: %+v ok=%v err=%v", luckin, ok, err)
	}

	// 12:09 +08 is 04:09Z, same Shanghai day as the legacy 10:15 upload.
	again, dup, err := st.InsertBill(ctx, NewBill{
		AmountCents: 1070,
		Currency:    "CNY",
		Merchant:    "沙县小吃",
		PaidAt:      time.Date(2026, 10, 26, 12, 9, 0, 0, shanghai(t)),
		ContentHash: strings.Repeat("c", 64),
	})
	if err != nil || !dup || again.ID != shaxian.ID {
		t.Fatalf("migrated row should match the real paid_at: %+v dup=%v err=%v", again, dup, err)
	}

	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	reopened, ok, err := st.FindByContentHash(ctx, strings.Repeat("a", 64))
	if err != nil || !ok || !reopened.PaidAtDefaulted {
		t.Fatalf("reopen: %+v ok=%v err=%v", reopened, ok, err)
	}
}

func shanghai(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}
