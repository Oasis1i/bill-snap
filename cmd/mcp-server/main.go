package main

import (
	"log/slog"
	"os"
	"strings"

	"github.com/Oasis1i/bill-snap/internal/config"
	"github.com/Oasis1i/bill-snap/internal/mcp"
	"github.com/Oasis1i/bill-snap/internal/model"
	"github.com/Oasis1i/bill-snap/internal/store"
)

func main() {
	// Logs go to stderr. stdout is reserved for JSON-RPC.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := run(); err != nil {
		slog.Error("mcp stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	st.SetLocation(cfg.Location)
	model.UseCategories(cfg.Categories)
	slog.Info("bill-snap mcp", "db", cfg.DBPath, "tz", cfg.Timezone, "categories", strings.Join(cfg.Categories, ","))
	return mcp.Serve(os.Stdin, os.Stdout, st, cfg.Location)
}
