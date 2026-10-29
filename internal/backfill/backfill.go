// Package backfill fills the items column from screenshots already on disk.
// It re-runs recognition and writes only items. Other columns stay as they are.
package backfill

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Oasis1i/bill-snap/internal/extract"
	"github.com/Oasis1i/bill-snap/internal/model"
	"github.com/Oasis1i/bill-snap/internal/store"
)

const (
	// StatusDryRun means recognition produced a product name and nothing was written.
	StatusDryRun = "dry-run"
	// StatusUpdated means items were written.
	StatusUpdated = "updated"
	// StatusSkipped means the row was left alone.
	StatusSkipped = "skipped"
	// StatusFailed means recognition or the write failed.
	StatusFailed = "failed"
)

// Options controls a backfill pass. Apply false is a dry run.
type Options struct {
	Apply    bool
	MaxBytes int64
}

// Outcome is one candidate row.
type Outcome struct {
	ID     int64
	Items  string
	Status string
	Detail string
}

// Run reads every bill whose items are empty and whose image_path is set.
// Missing files are skipped. A non-bill or an empty product name is skipped.
// Apply writes items and does not touch any other column.
func Run(ctx context.Context, st *store.Store, ex extract.Extractor, opt Options) ([]Outcome, error) {
	if st == nil {
		return nil, fmt.Errorf("nil store")
	}
	if ex == nil {
		return nil, fmt.Errorf("nil extractor")
	}
	if opt.MaxBytes <= 0 {
		opt.MaxBytes = 8 << 20
	}
	bills, err := st.ListItemsBackfill(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Outcome, 0, len(bills))
	for _, bill := range bills {
		out = append(out, one(ctx, st, ex, opt, bill))
	}
	return out, nil
}

func one(ctx context.Context, st *store.Store, ex extract.Extractor, opt Options, bill model.Bill) Outcome {
	path := strings.TrimSpace(bill.ImagePath)
	if path == "" {
		return Outcome{ID: bill.ID, Status: StatusSkipped, Detail: "没有图片"}
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return Outcome{ID: bill.ID, Status: StatusSkipped, Detail: "图片不存在"}
	}
	if info.Size() > opt.MaxBytes {
		return Outcome{ID: bill.ID, Status: StatusSkipped, Detail: "图片过大"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Outcome{ID: bill.ID, Status: StatusFailed, Detail: "图片无法读取"}
	}
	res, err := ex.Extract(ctx, data, sniffMIME(data))
	if err != nil {
		return Outcome{ID: bill.ID, Status: StatusFailed, Detail: "识别失败"}
	}
	if !res.IsBill {
		return Outcome{ID: bill.ID, Status: StatusSkipped, Detail: "不是账单"}
	}
	items := model.NormalizeItems(res.Items)
	if items == "" {
		return Outcome{ID: bill.ID, Status: StatusSkipped, Detail: "没有识别到商品名"}
	}
	if !opt.Apply {
		return Outcome{ID: bill.ID, Items: items, Status: StatusDryRun, Detail: "未写库"}
	}
	if err := st.UpdateItems(ctx, bill.ID, items); err != nil {
		return Outcome{ID: bill.ID, Items: items, Status: StatusFailed, Detail: "写入失败"}
	}
	return Outcome{ID: bill.ID, Items: items, Status: StatusUpdated, Detail: "已写入"}
}

func sniffMIME(data []byte) string {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return "image/png"
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg"
	case len(data) >= 12 && bytes.Equal(data[0:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}
