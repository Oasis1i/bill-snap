// Package api is the token-protected HTTP API.
package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/Oasis1i/bill-snap/internal/extract"
	"github.com/Oasis1i/bill-snap/internal/mcp"
	"github.com/Oasis1i/bill-snap/internal/store"
	"github.com/Oasis1i/bill-snap/internal/version"
)

// Options configures the HTTP handlers.
type Options struct {
	Token          string
	MCPToken       string
	UploadDir      string
	Location       *time.Location
	Store          *store.Store
	Extractor      extract.Extractor
	MaxUploadBytes int64
	Version        string
}

// Server serves health, upload, list, summary, and the remote MCP route.
type Server struct {
	token     string
	mcpToken  string
	uploadDir string
	loc       *time.Location
	store     *store.Store
	extract   extract.Extractor
	maxBytes  int64
	version   string
}

// New builds a Server. A nil location means UTC. A non-positive size limit
// means 8 MiB.
func New(opts Options) *Server {
	if opts.Location == nil {
		opts.Location = time.UTC
	}
	if opts.MaxUploadBytes <= 0 {
		opts.MaxUploadBytes = 8 << 20
	}
	if opts.Version == "" {
		opts.Version = version.Version
	}
	if opts.Store != nil {
		opts.Store.SetLocation(opts.Location)
	}
	return &Server{
		token:     opts.Token,
		mcpToken:  strings.TrimSpace(opts.MCPToken),
		uploadDir: opts.UploadDir,
		loc:       opts.Location,
		store:     opts.Store,
		extract:   opts.Extractor,
		maxBytes:  opts.MaxUploadBytes,
		version:   opts.Version,
	}
}

// Handler is the public mux. /healthz is open. Bill routes require the upload
// token. POST /mcp requires MCP_TOKEN and is 404 when that token is unset.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /api/v1/upload", s.withAuth(s.handleUpload))
	mux.HandleFunc("GET /api/v1/bills", s.withAuth(s.handleList))
	mux.HandleFunc("GET /api/v1/summary", s.withAuth(s.handleSummary))
	mux.Handle("/mcp", mcp.NewHandler(s.store, s.loc, s.mcpToken))
	return recoverMiddleware(mux)
}
