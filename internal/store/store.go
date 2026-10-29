// Package store persists bills in SQLite.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
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

const billColumns = `id, amount_cents, currency, merchant, category, paid_at, note, image_path, content_hash, dedup_key, raw_model_json, order_no, paid_at_defaulted, created_at, updated_at, items, source, request_id`

const (
	sourceScreenshot = "screenshot"
	sourceManual     = "manual"
)

// NewBill is the input for an insert. DedupKey is computed inside InsertBill
// so callers cannot drift from the documented key.
// OrderNo is normalized before it is stored. PaidAtDefaulted records that
// PaidAt was filled with the upload time because the model omitted it.
// Category is normalized to the effective category list. Empty or unknown
// values, including close variants, are stored as 其他.
type NewBill struct {
	AmountCents     int64
	Currency        string
	Merchant        string
	Category        string
	PaidAt          time.Time
	PaidAtDefaulted bool
	OrderNo         string
	Note            string
	Items           string
	ImagePath       string
	ContentHash     string
	RawModelJSON    string
}

// ManualBill is one add_bill row. It does not participate in screenshot dedup.
// ContentHash and DedupKey are filled with unique manual: placeholders so the
// unique indexes accept the row. RequestID empty is stored as NULL.
type ManualBill struct {
	AmountCents int64
	Merchant    string
	Category    string
	PaidAt      time.Time
	Items       string
	Note        string
	RequestID   string
}

// AuditWrite is one add_bill attempt. Result is "ok" or "error:"+message.
// BillID is nil when the attempt did not resolve to a row.
type AuditWrite struct {
	TokenKind string
	Tool      string
	ArgsJSON  string
	Result    string
	BillID    *int64
}

// AuditRow is a stored audit_log record.
type AuditRow struct {
	ID        int64
	At        string
	TokenKind string
	Tool      string
	ArgsJSON  string
	Result    string
	BillID    sql.NullInt64
}

// ListFilter selects bills.
// From is inclusive. To is exclusive. Both are compared to paid_at stored as
// UTC RFC3339. Category is an exact match when non-empty. It is not rewritten
// onto the fixed enum, so an unknown filter matches nothing.
// ExcludeCategory drops one exact category when non-empty. The HTTP API leaves
// it empty. summarize_spend sets it to 工资 unless the caller includes income.
type ListFilter struct {
	From            *time.Time
	To              *time.Time
	Category        string
	ExcludeCategory string
	Limit           int
}

// Store is a SQLite bill database. One connection keeps locking simple for a
// single-user process; separate HTTP and MCP processes share the file via WAL.
type Store struct {
	db  *sql.DB
	loc *time.Location
}

// defaultLocation is APP_TZ's default. SetLocation replaces it for a process
// that was configured with another zone.
var defaultLocation = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	return loc
}()

// Open creates the parent directory, opens (or creates) the database, and
// applies schema.sql. Local-day dedup uses Asia/Shanghai until SetLocation.
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
	s := &Store{db: db, loc: defaultLocation}
	if err := s.applySchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// SetLocation sets the time zone used when a defaulted paid_at is compared
// by calendar day. Nil keeps the current zone. The HTTP server passes APP_TZ.
func (s *Store) SetLocation(loc *time.Location) {
	if loc != nil {
		s.loc = loc
	}
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) applySchema(ctx context.Context) error {
	var tables, indexes []string
	for _, stmt := range splitSQL(schemaSQL) {
		if isIndexStatement(stmt) {
			indexes = append(indexes, stmt)
			continue
		}
		tables = append(tables, stmt)
	}
	if err := execStmts(ctx, s.db, tables); err != nil {
		return err
	}
	// Columns first. An existing database's CREATE TABLE is a no-op, so the
	// order_no index below would fail if it ran before the ALTER.
	if err := s.migrateBills(ctx); err != nil {
		return err
	}
	return execStmts(ctx, s.db, indexes)
}

func execStmts(ctx context.Context, db *sql.DB, stmts []string) error {
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("apply schema: %w", err)
		}
	}
	return nil
}

func isIndexStatement(stmt string) bool {
	for _, line := range strings.Split(stmt, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		upper := strings.ToUpper(line)
		return strings.HasPrefix(upper, "CREATE UNIQUE INDEX") || strings.HasPrefix(upper, "CREATE INDEX")
	}
	return false
}

func (s *Store) migrateBills(ctx context.Context) error {
	cols, err := billColumnSet(ctx, s.db)
	if err != nil {
		return err
	}
	if !cols["order_no"] {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE bills ADD COLUMN order_no TEXT`); err != nil {
			return fmt.Errorf("add order_no: %w", err)
		}
	}
	if !cols["paid_at_defaulted"] {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE bills ADD COLUMN paid_at_defaulted INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add paid_at_defaulted: %w", err)
		}
	}
	if !cols["items"] {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE bills ADD COLUMN items TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add items: %w", err)
		}
	}
	if !cols["source"] {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE bills ADD COLUMN source TEXT NOT NULL DEFAULT 'screenshot'`); err != nil {
			return fmt.Errorf("add source: %w", err)
		}
	}
	if !cols["request_id"] {
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE bills ADD COLUMN request_id TEXT`); err != nil {
			return fmt.Errorf("add request_id: %w", err)
		}
	}
	// Rows stored before the flag existed only recorded the default in note.
	if _, err := s.db.ExecContext(ctx, `
		UPDATE bills
		SET paid_at_defaulted = 1
		WHERE paid_at_defaulted = 0
		  AND instr(note, ?) > 0
	`, model.PaidAtDefaultedNote); err != nil {
		return fmt.Errorf("backfill paid_at_defaulted: %w", err)
	}
	return nil
}

func billColumnSet(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(bills)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var (
			cid     int
			name    string
			typ     string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
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

// InsertBill inserts a bill or returns the existing row when this payment was
// already stored. duplicate is true when no new row was written.
// The stored paid_at is UTC truncated to the minute. The existing row is not
// modified, so a repeat upload is idempotent.
//
// Match order:
//  1. content_hash
//  2. normalized order_no, when the new bill has one
//  3. dedup_key, unless that row has a different non-empty order number
//  4. same amount, currency, and normalized merchant on the same local day,
//     when this bill's paid_at was defaulted or the existing row's was.
//     Two different non-empty order numbers are not matched by this rule.
func (s *Store) InsertBill(ctx context.Context, in NewBill) (model.Bill, bool, error) {
	p, err := prepareBill(in)
	if err != nil {
		return model.Bill{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Bill{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	hit, found, storeKey, err := s.resolve(ctx, tx, p)
	if err != nil {
		return model.Bill{}, false, err
	}
	if found {
		return hit, true, nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	var orderNo any
	if p.OrderNo != "" {
		orderNo = p.OrderNo
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO bills (
		amount_cents, currency, merchant, category, paid_at, note, image_path,
		content_hash, dedup_key, raw_model_json, order_no, paid_at_defaulted,
		created_at, updated_at, items, source
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.AmountCents, p.Currency, p.Merchant, p.Category, p.PaidStr, p.Note,
		p.ImagePath, p.ContentHash, storeKey, p.RawModelJSON, orderNo,
		boolInt(p.PaidAtDefaulted), now, now, p.Items, p.Source,
	)
	if err != nil {
		if isUniqueViolation(err) {
			if rbErr := tx.Rollback(); rbErr != nil {
				return model.Bill{}, false, rbErr
			}
			return s.findExisting(ctx, p)
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

// FindDuplicate reports an already stored bill that matches in, using the
// same rules as InsertBill. It does not write.
func (s *Store) FindDuplicate(ctx context.Context, in NewBill) (model.Bill, bool, error) {
	p, err := prepareBill(in)
	if err != nil {
		return model.Bill{}, false, err
	}
	hit, found, _, err := s.resolve(ctx, s.db, p)
	return hit, found, err
}

type preparedBill struct {
	AmountCents     int64
	Currency        string
	Merchant        string
	MerchantKey     string
	Category        string
	PaidAt          time.Time
	PaidStr         string
	Note            string
	Items           string
	ImagePath       string
	ContentHash     string
	DedupKey        string
	RawModelJSON    string
	OrderNo         string
	PaidAtDefaulted bool
	Source          string
}

func prepareBill(in NewBill) (preparedBill, error) {
	hash := strings.TrimSpace(in.ContentHash)
	if hash == "" {
		return preparedBill{}, fmt.Errorf("content hash is required")
	}
	if in.PaidAt.IsZero() {
		return preparedBill{}, fmt.Errorf("paid_at is required")
	}
	if in.AmountCents < 0 {
		return preparedBill{}, fmt.Errorf("amount_cents must be >= 0")
	}
	currency := strings.ToUpper(strings.TrimSpace(in.Currency))
	if currency == "" {
		currency = "CNY"
	}
	merchant := strings.TrimSpace(in.Merchant)
	paid := model.NormalizePaidAt(in.PaidAt)
	return preparedBill{
		AmountCents:     in.AmountCents,
		Currency:        currency,
		Merchant:        merchant,
		MerchantKey:     model.NormalizeMerchant(merchant),
		Category:        model.NormalizeCategory(in.Category),
		PaidAt:          paid,
		PaidStr:         paid.Format(time.RFC3339),
		Note:            strings.TrimSpace(in.Note),
		Items:           model.NormalizeItems(in.Items),
		ImagePath:       in.ImagePath,
		ContentHash:     hash,
		DedupKey:        model.MakeDedupKey(in.AmountCents, currency, merchant, paid),
		RawModelJSON:    in.RawModelJSON,
		OrderNo:         model.NormalizeOrderNo(in.OrderNo),
		PaidAtDefaulted: in.PaidAtDefaulted,
		Source:          sourceScreenshot,
	}, nil
}

// resolve looks for an existing payment. storeKey is the dedup_key to write
// when found is false. It stays the plain key unless that key already belongs
// to a different order number.
func (s *Store) resolve(ctx context.Context, q queryer, in preparedBill) (model.Bill, bool, string, error) {
	storeKey := in.DedupKey
	if b, ok, err := findOne(ctx, q, "content_hash = ?", in.ContentHash); err != nil || ok {
		return b, ok, storeKey, err
	}
	if in.OrderNo != "" {
		if b, ok, err := findOne(ctx, q, "order_no = ?", in.OrderNo); err != nil || ok {
			return b, ok, storeKey, err
		}
	}

	legacy, ok, err := findOne(ctx, q, "dedup_key = ?", in.DedupKey)
	if err != nil {
		return model.Bill{}, false, storeKey, err
	}
	if ok && !orderConflict(in.OrderNo, legacy.OrderNo) {
		return legacy, true, storeKey, nil
	}
	if ok && orderConflict(in.OrderNo, legacy.OrderNo) {
		storeKey = model.MakeDistinctOrderDedupKey(in.AmountCents, in.Currency, in.Merchant, in.PaidAt, in.OrderNo)
		alt, altOK, err := findOne(ctx, q, "dedup_key = ?", storeKey)
		if err != nil || altOK {
			return alt, altOK, storeKey, err
		}
	}

	hit, found, err := s.matchDefaultedDay(ctx, q, in)
	if err != nil || found {
		return hit, found, storeKey, err
	}
	return model.Bill{}, false, storeKey, nil
}

func (s *Store) matchDefaultedDay(ctx context.Context, q queryer, in preparedBill) (model.Bill, bool, error) {
	// Incoming defaulted: every row with this amount and currency is a candidate.
	// Incoming has a real time: only rows whose paid_at was defaulted are.
	rows, err := q.QueryContext(ctx, `SELECT `+billColumns+` FROM bills
		WHERE amount_cents = ? AND currency = ? AND (paid_at_defaulted = 1 OR ?)
		ORDER BY id ASC`, in.AmountCents, in.Currency, boolInt(in.PaidAtDefaulted))
	if err != nil {
		return model.Bill{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		b, err := scanBill(rows)
		if err != nil {
			return model.Bill{}, false, err
		}
		if model.NormalizeMerchant(b.Merchant) != in.MerchantKey {
			continue
		}
		if orderConflict(in.OrderNo, b.OrderNo) {
			continue
		}
		if !in.PaidAtDefaulted && !b.PaidAtDefaulted {
			continue
		}
		paid, err := time.Parse(time.RFC3339, b.PaidAt)
		if err != nil {
			return model.Bill{}, false, fmt.Errorf("bill %d paid_at: %w", b.ID, err)
		}
		if !model.SameLocalDay(in.PaidAt, paid, s.loc) {
			continue
		}
		return b, true, nil
	}
	if err := rows.Err(); err != nil {
		return model.Bill{}, false, err
	}
	return model.Bill{}, false, nil
}

func orderConflict(a, b string) bool {
	return a != "" && b != "" && a != b
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (s *Store) findExisting(ctx context.Context, in preparedBill) (model.Bill, bool, error) {
	hit, found, _, err := s.resolve(ctx, s.db, in)
	if err != nil {
		return model.Bill{}, false, err
	}
	if !found {
		return model.Bill{}, false, fmt.Errorf("unique conflict but no existing bill for that hash, order number, or dedup key")
	}
	return hit, true, nil
}

// FindByContentHash returns the bill stored for these exact image bytes.
func (s *Store) FindByContentHash(ctx context.Context, hash string) (model.Bill, bool, error) {
	return findOne(ctx, s.db, "content_hash = ?", hash)
}

// FindByDedupKey returns the bill for this payment key.
func (s *Store) FindByDedupKey(ctx context.Context, key string) (model.Bill, bool, error) {
	return findOne(ctx, s.db, "dedup_key = ?", key)
}

// FindByID returns the bill with this primary key.
func (s *Store) FindByID(ctx context.Context, id int64) (model.Bill, bool, error) {
	return findOne(ctx, s.db, "id = ?", id)
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func findOne(ctx context.Context, q queryer, where string, args ...any) (model.Bill, bool, error) {
	row := q.QueryRowContext(ctx, `SELECT `+billColumns+` FROM bills WHERE `+where+` LIMIT 1`, args...)
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
	var order, requestID sql.NullString
	var defaulted int
	err := sc.Scan(
		&b.ID, &b.AmountCents, &b.Currency, &b.Merchant, &b.Category,
		&b.PaidAt, &b.Note, &b.ImagePath, &b.ContentHash, &b.DedupKey,
		&b.RawModelJSON, &order, &defaulted, &b.CreatedAt, &b.UpdatedAt,
		&b.Items, &b.Source, &requestID,
	)
	if err != nil {
		return model.Bill{}, err
	}
	b.Amount = model.FormatCents(b.AmountCents)
	if order.Valid {
		b.OrderNo = order.String
	}
	if requestID.Valid {
		b.RequestID = requestID.String
	}
	b.PaidAtDefaulted = defaulted != 0
	if b.Source == "" {
		b.Source = sourceScreenshot
	}
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

// SpendLine is one bill reduced to the fields a read-only summary needs.
// Screenshot paths and model payloads are not selected.
// Limit on the filter is ignored so a summary can see every matching row.
type SpendLine struct {
	PaidAt      string
	AmountCents int64
	Category    string
}

// SpendLines returns matching bills oldest payment first.
func (s *Store) SpendLines(ctx context.Context, f ListFilter) ([]SpendLine, error) {
	where, args := f.where()
	rows, err := s.db.QueryContext(ctx, `SELECT paid_at, amount_cents, category FROM bills WHERE `+where+` ORDER BY paid_at ASC, id ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SpendLine{}
	for rows.Next() {
		var line SpendLine
		if err := rows.Scan(&line.PaidAt, &line.AmountCents, &line.Category); err != nil {
			return nil, err
		}
		out = append(out, line)
	}
	return out, rows.Err()
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
	if cat := strings.TrimSpace(f.ExcludeCategory); cat != "" {
		parts = append(parts, "category != ?")
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

func uniqueKind(err error) string {
	if !isUniqueViolation(err) {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "request_id"):
		return "request_id"
	case strings.Contains(msg, "content_hash"), strings.Contains(msg, "dedup_key"):
		return "key"
	default:
		return "other"
	}
}

// InsertManualBill writes a manual bill and an ok audit row.
// Screenshot content_hash / order_no / dedup_key matching is not used.
// created is false when request_id already belongs to a row; that row is
// returned unchanged. A unique request_id race is treated the same way.
func (s *Store) InsertManualBill(ctx context.Context, in ManualBill, kind, tool, argsJSON string) (model.Bill, bool, error) {
	if in.AmountCents <= 0 {
		return model.Bill{}, false, fmt.Errorf("amount_cents must be > 0")
	}
	if in.PaidAt.IsZero() {
		return model.Bill{}, false, fmt.Errorf("paid_at is required")
	}
	requestID := strings.TrimSpace(in.RequestID)
	if requestID != "" {
		existing, ok, err := s.FindByRequestID(ctx, requestID)
		if err != nil {
			return model.Bill{}, false, err
		}
		if ok {
			if err := s.WriteAudit(ctx, AuditWrite{
				TokenKind: kind, Tool: tool, ArgsJSON: argsJSON, Result: "ok", BillID: &existing.ID,
			}); err != nil {
				return model.Bill{}, false, err
			}
			return existing, false, nil
		}
	}

	paid := model.NormalizePaidAt(in.PaidAt)
	merchant := strings.TrimSpace(in.Merchant)
	note := strings.TrimSpace(in.Note)
	items := model.NormalizeItems(in.Items)
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		hash, err := newManualToken()
		if err != nil {
			return model.Bill{}, false, err
		}
		dedup, err := newManualToken()
		if err != nil {
			return model.Bill{}, false, err
		}
		bill, created, kindHit, err := s.insertManualOnce(ctx, manualInsert{
			AmountCents: in.AmountCents,
			Merchant:    merchant,
			Category:    strings.TrimSpace(in.Category),
			PaidStr:     paid.Format(time.RFC3339),
			Note:        note,
			Items:       items,
			ContentHash: hash,
			DedupKey:    dedup,
			RequestID:   requestID,
			Kind:        kind,
			Tool:        tool,
			ArgsJSON:    argsJSON,
		})
		if err == nil {
			return bill, created, nil
		}
		if kindHit == "request_id" && requestID != "" {
			existing, ok, findErr := s.FindByRequestID(ctx, requestID)
			if findErr != nil {
				return model.Bill{}, false, findErr
			}
			if !ok {
				return model.Bill{}, false, err
			}
			if auditErr := s.WriteAudit(ctx, AuditWrite{
				TokenKind: kind, Tool: tool, ArgsJSON: argsJSON, Result: "ok", BillID: &existing.ID,
			}); auditErr != nil {
				return model.Bill{}, false, auditErr
			}
			return existing, false, nil
		}
		if kindHit == "key" {
			lastErr = err
			continue
		}
		return model.Bill{}, false, err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("manual bill key collision")
	}
	return model.Bill{}, false, lastErr
}

type manualInsert struct {
	AmountCents int64
	Merchant    string
	Category    string
	PaidStr     string
	Note        string
	Items       string
	ContentHash string
	DedupKey    string
	RequestID   string
	Kind        string
	Tool        string
	ArgsJSON    string
}

func (s *Store) insertManualOnce(ctx context.Context, in manualInsert) (model.Bill, bool, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Bill{}, false, "", err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Format(time.RFC3339)
	var requestID any
	if in.RequestID != "" {
		requestID = in.RequestID
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO bills (
		amount_cents, currency, merchant, category, paid_at, note, image_path,
		content_hash, dedup_key, raw_model_json, order_no, paid_at_defaulted,
		created_at, updated_at, items, source, request_id
	) VALUES (?, 'CNY', ?, ?, ?, ?, '', ?, ?, '', NULL, 0, ?, ?, ?, ?, ?)`,
		in.AmountCents, in.Merchant, in.Category, in.PaidStr, in.Note,
		in.ContentHash, in.DedupKey, now, now, in.Items, sourceManual, requestID,
	)
	if err != nil {
		return model.Bill{}, false, uniqueKind(err), err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Bill{}, false, "", err
	}
	if err := insertAudit(ctx, tx, AuditWrite{
		TokenKind: in.Kind, Tool: in.Tool, ArgsJSON: in.ArgsJSON, Result: "ok", BillID: &id,
	}); err != nil {
		return model.Bill{}, false, "", err
	}
	b, ok, err := findOne(ctx, tx, "id = ?", id)
	if err != nil {
		return model.Bill{}, false, "", err
	}
	if !ok {
		return model.Bill{}, false, "", fmt.Errorf("inserted bill %d was not readable", id)
	}
	if err := tx.Commit(); err != nil {
		return model.Bill{}, false, "", err
	}
	return b, true, "", nil
}

func newManualToken() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "manual:" + hex.EncodeToString(buf[:]), nil
}

// FindByRequestID returns the manual bill stored under this idempotency key.
func (s *Store) FindByRequestID(ctx context.Context, requestID string) (model.Bill, bool, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return model.Bill{}, false, nil
	}
	return findOne(ctx, s.db, "request_id = ?", requestID)
}

// FindSameDayAmountCategory returns the smallest id with this amount, category,
// and local calendar day. Currency is CNY. It does not write.
func (s *Store) FindSameDayAmountCategory(ctx context.Context, cents int64, category string, paid time.Time) (int64, bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, paid_at FROM bills WHERE amount_cents = ? AND category = ? AND currency = 'CNY' ORDER BY id ASC`, cents, strings.TrimSpace(category))
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var paidStr string
		if err := rows.Scan(&id, &paidStr); err != nil {
			return 0, false, err
		}
		got, err := time.Parse(time.RFC3339, paidStr)
		if err != nil {
			return 0, false, fmt.Errorf("bill %d paid_at: %w", id, err)
		}
		if model.SameLocalDay(paid, got, s.loc) {
			return id, true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return 0, false, err
	}
	return 0, false, nil
}

// UpdateItems sets items on one row and leaves every other column unchanged,
// including updated_at. A row whose items are already non-empty is not touched.
func (s *Store) UpdateItems(ctx context.Context, id int64, items string) error {
	items = model.NormalizeItems(items)
	if items == "" {
		return fmt.Errorf("items are empty")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE bills SET items = ? WHERE id = ? AND items = ''`, items, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("bill %d items were not updated", id)
	}
	return nil
}

// ListItemsBackfill returns rows whose items are empty and that still point at a path.
func (s *Store) ListItemsBackfill(ctx context.Context) ([]model.Bill, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+billColumns+` FROM bills WHERE items = '' AND trim(image_path) != '' ORDER BY id ASC`)
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

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// WriteAudit appends one audit row. The token value is not a column.
func (s *Store) WriteAudit(ctx context.Context, a AuditWrite) error {
	return insertAudit(ctx, s.db, a)
}

func insertAudit(ctx context.Context, ex execer, a AuditWrite) error {
	if strings.TrimSpace(a.Tool) == "" {
		a.Tool = "add_bill"
	}
	if strings.TrimSpace(a.ArgsJSON) == "" {
		a.ArgsJSON = "{}"
	}
	var billID any
	if a.BillID != nil {
		billID = *a.BillID
	}
	_, err := ex.ExecContext(ctx, `INSERT INTO audit_log (at, token_kind, tool, args_json, result, bill_id) VALUES (?, ?, ?, ?, ?, ?)`,
		time.Now().UTC().Format(time.RFC3339), a.TokenKind, a.Tool, a.ArgsJSON, a.Result, billID)
	return err
}

// ListAudits returns audit rows oldest first. MCP does not expose this.
func (s *Store) ListAudits(ctx context.Context) ([]AuditRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, at, token_kind, tool, args_json, result, bill_id FROM audit_log ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditRow{}
	for rows.Next() {
		var row AuditRow
		if err := rows.Scan(&row.ID, &row.At, &row.TokenKind, &row.Tool, &row.ArgsJSON, &row.Result, &row.BillID); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
