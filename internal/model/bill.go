package model

// Bill is one stored payment receipt.
// Amount is yuan as a two-decimal string ("25.90") for clients that display
// the value directly. AmountCents is the same value in fen and stays for
// older clients. Storage is still amount_cents.
// PaidAt, CreatedAt, and UpdatedAt are UTC RFC3339. PaidAt is truncated to the minute.
// OrderNo is the normalized order or transaction number, or empty when the
// screenshot did not show one. PaidAtDefaulted is true when PaidAt was filled
// with the upload time because the model omitted it.
// Category is one of the effective category list. Empty or unknown extractor output is 其他.
type Bill struct {
	ID              int64  `json:"id"`
	AmountCents     int64  `json:"amount_cents"`
	Amount          string `json:"amount"`
	Currency        string `json:"currency"`
	Merchant        string `json:"merchant"`
	Category        string `json:"category"`
	PaidAt          string `json:"paid_at"`
	PaidAtDefaulted bool   `json:"paid_at_defaulted"`
	OrderNo         string `json:"order_no"`
	Note            string `json:"note"`
	ImagePath       string `json:"image_path"`
	ContentHash     string `json:"content_hash"`
	DedupKey        string `json:"dedup_key"`
	RawModelJSON    string `json:"raw_model_json,omitempty"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

// BillList is the list_bills / GET /api/v1/bills payload.
type BillList struct {
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
	Category string `json:"category,omitempty"`
	Count    int    `json:"count"`
	Bills    []Bill `json:"bills"`
}

// MoneyTotal is a sum in one currency.
type MoneyTotal struct {
	Currency    string `json:"currency"`
	AmountCents int64  `json:"amount_cents"`
	Amount      string `json:"amount"`
}

// CategoryTotal is a sum for one category and currency.
type CategoryTotal struct {
	Category    string `json:"category"`
	Currency    string `json:"currency"`
	Count       int    `json:"count"`
	AmountCents int64  `json:"amount_cents"`
	Amount      string `json:"amount"`
}

// Summary is the summarize_spend / GET /api/v1/summary payload.
type Summary struct {
	From       string          `json:"from,omitempty"`
	To         string          `json:"to,omitempty"`
	Category   string          `json:"category,omitempty"`
	Count      int             `json:"count"`
	Totals     []MoneyTotal    `json:"totals"`
	ByCategory []CategoryTotal `json:"by_category"`
}
