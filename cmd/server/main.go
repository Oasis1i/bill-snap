package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Oasis1i/bill-snap/internal/api"
	"github.com/Oasis1i/bill-snap/internal/config"
	"github.com/Oasis1i/bill-snap/internal/extract"
	"github.com/Oasis1i/bill-snap/internal/model"
	"github.com/Oasis1i/bill-snap/internal/store"
	"github.com/Oasis1i/bill-snap/internal/version"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := run(); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.ValidateForServer(); err != nil {
		return err
	}
	model.UseCategories(cfg.Categories)
	if len(cfg.UploadToken) < 8 || cfg.UploadToken == "change-me" {
		slog.Warn("UPLOAD_TOKEN looks weak; use a long random value before exposing the server")
	}

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
	if err := os.MkdirAll(cfg.UploadDir, 0o755); err != nil {
		return err
	}

	srv := api.New(api.Options{
		Token:          cfg.UploadToken,
		UploadDir:      cfg.UploadDir,
		Location:       cfg.Location,
		Store:          st,
		Extractor:      ex,
		MaxUploadBytes: cfg.MaxUploadBytes,
		Version:        version.Version,
	})
	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.HTTPAddr, "provider", cfg.VisionProvider, "db", cfg.DBPath, "categories", strings.Join(cfg.Categories, ","))
		errCh <- httpSrv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		sh, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpSrv.Shutdown(sh)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
