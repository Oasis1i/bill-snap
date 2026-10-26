// Package store persists bills in SQLite.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Oasis1i/bill-snap/internal/model"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

const billColumns = `id, amount_cents, currency, merchant, category, paid_at, note, image_path, content_hash, dedup_key, raw_model_json, created_at, updated_at`

// NewBill is the input for an insert. DedupKey is computed inside InsertBill
// so callers cannot drift from the documented key.
type NewBill struct {
	AmountCents  int64
	Currency     string
	Merchant     string
	Category     string
	PaidAt       time.Time
	Note         string
	ImagePath    string
	ContentHash  string
	RawModelJSON string
}

// ListFilter selects bills.
// From is inclusive. To is exclusive. Both are compared to paid_at stored as
// UTC RFC3339. Category is an exact match when non-empty.
type ListFilter struct {
	From     *time.Time
	To       *time.Time
	Category string
	Limit    int
}

// Store is a SQLite bill database. One connection keeps locking simple for a
// single-user process; separate HTTP and MCP processes share the file via WAL.
type Store struct {
	db *sql.DB
}

// Open creates the parent directory, opens (or creates) the database, and
// applies schema.sql.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("db path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// Serialize statements in this process. The HTTP server and the MCP
	// process are separate connections; busy_timeout covers that.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err := s.applySchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) applySchema(ctx context.Context) error {
	for _, stmt := range splitSQL(schemaSQL) {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("apply schema: %w", err)
		}
	}
	return nil
}

func splitSQL(sqlText string) []string {
	var out []string
	for _, part := range strings.Split(sqlText, ";") {
		stmt := strings.TrimSpace(part)
		if stmt == "" || onlyComments(stmt) {
			continue
		}
		out = append(out, stmt)
	}
	return out
}

func onlyComments(stmt string) bool {
	for _, line := range strings.Split(stmt, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		return false
	}
	return true
}

// InsertBill inserts a bill or returns the existing row when content_hash or
// dedup_key already exists. duplicate is true when no new row was written.
// The stored paid_at is UTC truncated to the minute. The existing row is not
// modified, so a repeat upload is idempotent.
func (s *Store) InsertBill(ctx context.Context, in NewBill) (model.Bill, bool, error) {
	if strings.TrimSpace(in.ContentHash) == "" {
		return model.Bill{}, false, fmt.Errorf("content hash is required")
	}
	if in.PaidAt.IsZero() {
		return model.Bill{}, false, fmt.Errorf("paid_at is required")
	}
	if in.AmountCents < 0 {
		return model.Bill{}, false, fmt.Errorf("amount_cents must be >= 0")
	}
	currency := strings.ToUpper(strings.TrimSpace(in.Currency))
	if currency == "" {
		currency = "CNY"
	}
	merchant := strings.TrimSpace(in.Merchant)
	category := strings.TrimSpace(in.Category)
	paid := model.NormalizePaidAt(in.PaidAt)
	dedup := model.MakeDedupKey(in.AmountCents, currency, merchant, paid)
	now := time.Now().UTC().Format(time.RFC3339)
	paidStr := paid.Format(time.RFC3339)
	hash := strings.TrimSpace(in.ContentHash)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Bill{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	if b, ok, err := findOne(ctx, tx, "content_hash = ?", hash); err != nil || ok {
		return b, ok, err
	}
	if b, ok, err := findOne(ctx, tx, "dedup_key = ?", dedup); err != nil || ok {
		return b, ok, err
	}

	res, err := tx.ExecContext(ctx, `INSERT INTO bills (
		amount_cents, currency, merchant, category, paid_at, note, image_path,
		content_hash, dedup_key, raw_model_json, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.AmountCents, currency, merchant, category, paidStr, strings.TrimSpace(in.Note),
		in.ImagePath, hash, dedup, in.RawModelJSON, now, now,
	)
	if err != nil {
		if isUniqueViolation(err) {
			if rbErr := tx.Rollback(); rbErr != nil {
				return model.Bill{}, false, rbErr
			}
			return s.findExisting(ctx, hash, dedup)
		}
		return model.Bill{}, false, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Bill{}, false, err
	}
	b, ok, err := findOne(ctx, tx, "id = ?", id)
	if err != nil {
		return model.Bill{}, false, err
	}
	if !ok {
		return model.Bill{}, false, fmt.Errorf("inserted bill %d was not readable", id)
	}
	if err := tx.Commit(); err != nil {
		return model.Bill{}, false, err
	}
	return b, false, nil
}

func (s *Store) findExisting(ctx context.Context, hash, dedup string) (model.Bill, bool, error) {
	if b, ok, err := s.FindByContentHash(ctx, hash); err != nil || ok {
		return b, ok, err
	}
	if b, ok, err := s.FindByDedupKey(ctx, dedup); err != nil || ok {
		return b, ok, err
	}
	return model.Bill{}, false, fmt.Errorf("unique conflict but no existing bill for that hash or dedup key")
}

// FindByContentHash returns the bill stored for these exact image bytes.
func (s *Store) FindByContentHash(ctx context.Context, hash string) (model.Bill, bool, error) {
	return findOne(ctx, s.db, "content_hash = ?", hash)
}

// FindByDedupKey returns the bill for this payment key.
func (s *Store) FindByDedupKey(ctx context.Context, key string) (model.Bill, bool, error) {
	return findOne(ctx, s.db, "dedup_key = ?", key)
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func findOne(ctx context.Context, q queryRower, where string, arg any) (model.Bill, bool, error) {
	row := q.QueryRowContext(ctx, `SELECT `+billColumns+` FROM bills WHERE `+where+` LIMIT 1`, arg)
	b, err := scanBill(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Bill{}, false, nil
	}
	if err != nil {
		return model.Bill{}, false, err
	}
	return b, true, nil
}

func scanBill(sc interface{ Scan(dest ...any) error }) (model.Bill, error) {
	var b model.Bill
	err := sc.Scan(
		&b.ID, &b.AmountCents, &b.Currency, &b.Merchant, &b.Category,
		&b.PaidAt, &b.Note, &b.ImagePath, &b.ContentHash, &b.DedupKey,
		&b.RawModelJSON, &b.CreatedAt, &b.UpdatedAt,
	)
	if err != nil {
		return model.Bill{}, err
	}
	b.Amount = model.FormatCents(b.AmountCents)
	return b, nil
}

// List returns bills matching f, newest payment first.
func (s *Store) List(ctx context.Context, f ListFilter) ([]model.Bill, error) {
	where, args := f.where()
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT `+billColumns+` FROM bills WHERE `+where+` ORDER BY paid_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Bill{}
	for rows.Next() {
		b, err := scanBill(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Summarize aggregates amount_cents for f. Limit is ignored.
func (s *Store) Summarize(ctx context.Context, f ListFilter) (model.Summary, error) {
	where, args := f.where()
	rows, err := s.db.QueryContext(ctx, `
		SELECT category, currency, COUNT(*), COALESCE(SUM(amount_cents), 0)
		FROM bills
		WHERE `+where+`
		GROUP BY category, currency
		ORDER BY category ASC, currency ASC`, args...)
	if err != nil {
		return model.Summary{}, err
	}
	defer rows.Close()

	out := model.Summary{
		Totals:     []model.MoneyTotal{},
		ByCategory: []model.CategoryTotal{},
	}
	totals := map[string]int64{}
	for rows.Next() {
		var cat, cur string
		var n int
		var sum int64
		if err := rows.Scan(&cat, &cur, &n, &sum); err != nil {
			return model.Summary{}, err
		}
		out.Count += n
		totals[cur] += sum
		out.ByCategory = append(out.ByCategory, model.CategoryTotal{
			Category:    cat,
			Currency:    cur,
			Count:       n,
			AmountCents: sum,
			Amount:      model.FormatCents(sum),
		})
	}
	if err := rows.Err(); err != nil {
		return model.Summary{}, err
	}
	currencies := make([]string, 0, len(totals))
	for cur := range totals {
		currencies = append(currencies, cur)
	}
	sort.Strings(currencies)
	for _, cur := range currencies {
		out.Totals = append(out.Totals, model.MoneyTotal{
			Currency:    cur,
			AmountCents: totals[cur],
			Amount:      model.FormatCents(totals[cur]),
		})
	}
	return out, nil
}

func (f ListFilter) where() (string, []any) {
	parts := []string{"1=1"}
	var args []any
	if f.From != nil {
		parts = append(parts, "paid_at >= ?")
		args = append(args, f.From.UTC().Format(time.RFC3339))
	}
	if f.To != nil {
		parts = append(parts, "paid_at < ?")
		args = append(args, f.To.UTC().Format(time.RFC3339))
	}
	if cat := strings.TrimSpace(f.Category); cat != "" {
		parts = append(parts, "category = ?")
		args = append(args, cat)
	}
	return strings.Join(parts, " AND "), args
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "unique")
}
