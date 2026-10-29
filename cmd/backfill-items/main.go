// Command backfill-items re-runs receipt recognition for stored screenshots
// whose items column is still empty. The default is a dry run. -apply writes
// items and no other column.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/Oasis1i/bill-snap/internal/backfill"
	"github.com/Oasis1i/bill-snap/internal/config"
	"github.com/Oasis1i/bill-snap/internal/extract"
	"github.com/Oasis1i/bill-snap/internal/model"
	"github.com/Oasis1i/bill-snap/internal/store"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	apply := flag.Bool("apply", false, "把识别到的商品名写回 items。默认只打印，不改库")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "用法: backfill-items [-apply]\n\n对 items 为空、且 image_path 仍指向文件的账单重跑识别，只补商品名。\n默认不写库。数据库、时区和视觉模型与主服务相同（DB_PATH、APP_TZ、VISION_*、UPLOAD 目录的相对路径按当前工作目录解析）。\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if err := run(*apply); err != nil {
		slog.Error("backfill-items", "err", err)
		os.Exit(1)
	}
}

func run(apply bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	model.UseCategories(cfg.Categories)
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	st.SetLocation(cfg.Location)
	ex, err := extract.New(extract.Options{
		Provider: cfg.VisionProvider,
		APIKey:   cfg.VisionAPIKey,
		BaseURL:  cfg.VisionBaseURL,
		Model:    cfg.VisionModel,
		Location: cfg.Location,
	})
	if err != nil {
		return err
	}
	if apply {
		fmt.Println("apply：只更新 items，其它字段不动。")
	} else {
		fmt.Println("dry-run：不会写库。确认后加 -apply。")
	}
	rows, err := backfill.Run(context.Background(), st, ex, backfill.Options{
		Apply:    apply,
		MaxBytes: cfg.MaxUploadBytes,
	})
	if err != nil {
		return err
	}
	failed := 0
	for _, row := range rows {
		switch row.Status {
		case backfill.StatusDryRun:
			fmt.Printf("账单 %d：将写入「%s」\n", row.ID, row.Items)
		case backfill.StatusUpdated:
			fmt.Printf("账单 %d：已写入「%s」\n", row.ID, row.Items)
		case backfill.StatusFailed:
			failed++
			fmt.Printf("账单 %d：%s\n", row.ID, row.Detail)
			slog.Error("backfill item", "id", row.ID, "detail", row.Detail)
		default:
			fmt.Printf("账单 %d：%s，已跳过\n", row.ID, row.Detail)
		}
	}
	if len(rows) == 0 {
		fmt.Println("没有需要补商品名的账单。")
	}
	if failed > 0 {
		return fmt.Errorf("%d 笔识别或写入失败", failed)
	}
	return nil
}
