package backfill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Oasis1i/bill-snap/internal/extract"
	"github.com/Oasis1i/bill-snap/internal/model"
	"github.com/Oasis1i/bill-snap/internal/store"
	"github.com/Oasis1i/bill-snap/internal/testimg"
)

type fakeExtractor struct {
	calls int
	res   extract.Result
	err   error
}

func (f *fakeExtractor) Extract(context.Context, []byte, string) (extract.Result, error) {
	f.calls++
	if f.err != nil {
		return extract.Result{}, f.err
	}
	return f.res, nil
}

func TestDryRunDoesNotWriteApplyUpdatesOnlyItems(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bills.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	img, err := testimg.NoisyPNG(80, 80, 2)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(path, img, 0o644); err != nil {
		t.Fatal(err)
	}
	paid := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	original, _, err := st.InsertBill(context.Background(), store.NewBill{
		AmountCents: 1850,
		Currency:    "CNY",
		Merchant:    "瑞幸咖啡",
		Category:    "餐饮",
		PaidAt:      paid,
		Note:        "午饭",
		ImagePath:   path,
		ContentHash: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if original.Items != "" || original.Source != "screenshot" {
		t.Fatalf("%+v", original)
	}
	kept, _, err := st.InsertBill(context.Background(), store.NewBill{
		AmountCents: 900,
		Currency:    "CNY",
		Merchant:    "已有商品",
		Category:    "日用",
		PaidAt:      paid,
		Items:       "牛奶",
		ImagePath:   path + ".unused",
		ContentHash: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	noImage, _, err := st.InsertBill(context.Background(), store.NewBill{
		AmountCents: 100,
		Currency:    "CNY",
		Merchant:    "无图",
		Category:    "其他",
		PaidAt:      paid.Add(time.Hour),
		ContentHash: strings.Repeat("c", 64),
	})
	if err != nil {
		t.Fatal(err)
	}

	long := strings.Repeat("椰", 99) + "🙂多余"
	fake := &fakeExtractor{res: extract.Result{IsBill: true, Items: "  " + long + "  "}}
	dry, err := Run(context.Background(), st, fake, Options{Apply: false})
	if err != nil {
		t.Fatal(err)
	}
	if fake.calls != 1 || len(dry) != 1 || dry[0].ID != original.ID || dry[0].Status != StatusDryRun {
		t.Fatalf("calls %d outcomes %+v", fake.calls, dry)
	}
	if utf8RuneCount(dry[0].Items) != model.MaxItemRunes || !strings.HasSuffix(dry[0].Items, "🙂") {
		t.Fatalf("dry items %q", dry[0].Items)
	}
	afterDry, ok, err := st.FindByID(context.Background(), original.ID)
	if err != nil || !ok || afterDry.Items != "" || afterDry.UpdatedAt != original.UpdatedAt || afterDry.AmountCents != 1850 || afterDry.Note != "午饭" {
		t.Fatalf("%+v %v", afterDry, err)
	}

	applied, err := Run(context.Background(), st, fake, Options{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if fake.calls != 2 || len(applied) != 1 || applied[0].Status != StatusUpdated || applied[0].Items != dry[0].Items {
		t.Fatalf("calls %d %+v", fake.calls, applied)
	}
	after, ok, err := st.FindByID(context.Background(), original.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if after.Items != applied[0].Items || after.AmountCents != original.AmountCents || after.Merchant != original.Merchant || after.Category != original.Category || after.PaidAt != original.PaidAt || after.Note != original.Note || after.ImagePath != original.ImagePath || after.UpdatedAt != original.UpdatedAt || after.Source != "screenshot" || after.ContentHash != original.ContentHash {
		t.Fatalf("other fields changed\nbefore %+v\nafter  %+v", original, after)
	}
	still, ok, err := st.FindByID(context.Background(), kept.ID)
	if err != nil || !ok || still.Items != "牛奶" {
		t.Fatalf("filled row changed %+v", still)
	}
	empty, ok, err := st.FindByID(context.Background(), noImage.ID)
	if err != nil || !ok || empty.Items != "" {
		t.Fatalf("imageless row %+v", empty)
	}

	again, err := Run(context.Background(), st, fake, Options{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 || fake.calls != 2 {
		t.Fatalf("second apply calls %d rows %+v", fake.calls, again)
	}
}

func TestBackfillSkipsMissingNotBillAndEmpty(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "bills.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	paid := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	missing, _, err := st.InsertBill(context.Background(), store.NewBill{
		AmountCents: 100, Currency: "CNY", Category: "其他", PaidAt: paid,
		ImagePath: filepath.Join(dir, "gone.png"), ContentHash: strings.Repeat("d", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	img, err := testimg.NoisyPNG(80, 80, 3)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "real.png")
	if err := os.WriteFile(path, img, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.InsertBill(context.Background(), store.NewBill{
		AmountCents: 200, Currency: "CNY", Category: "其他", PaidAt: paid.Add(time.Hour),
		ImagePath: path, ContentHash: strings.Repeat("e", 64),
	}); err != nil {
		t.Fatal(err)
	}
	fake := &fakeExtractor{res: extract.Result{IsBill: false, Items: "不该写入"}}
	out, err := Run(context.Background(), st, fake, Options{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if fake.calls != 1 || len(out) != 2 {
		t.Fatalf("calls %d %+v", fake.calls, out)
	}
	if out[0].ID != missing.ID || out[0].Status != StatusSkipped || out[0].Detail != "图片不存在" {
		t.Fatalf("%+v", out[0])
	}
	if out[1].Status != StatusSkipped || out[1].Detail != "不是账单" {
		t.Fatalf("%+v", out[1])
	}
	row, ok, err := st.FindByID(context.Background(), out[1].ID)
	if err != nil || !ok || row.Items != "" {
		t.Fatalf("%+v", row)
	}

	fake.res = extract.Result{IsBill: true, Items: "   "}
	out, err = Run(context.Background(), st, fake, Options{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if fake.calls != 2 || len(out) != 2 {
		t.Fatalf("calls %d %+v", fake.calls, out)
	}
	var emptyItem bool
	for _, row := range out {
		if row.Detail == "没有识别到商品名" && row.Status == StatusSkipped {
			emptyItem = true
		}
	}
	if !emptyItem {
		t.Fatalf("%+v", out)
	}
}

func utf8RuneCount(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
