package model

// Bill is one stored payment receipt.
// Amount is major units as a decimal string ("18.50"). AmountCents is minor units.
// PaidAt, CreatedAt, and UpdatedAt are UTC RFC3339. PaidAt is truncated to the minute.
type Bill struct {
	ID           int64  `json:"id"`
	AmountCents  int64  `json:"amount_cents"`
	Amount       string `json:"amount"`
	Currency     string `json:"currency"`
	Merchant     string `json:"merchant"`
	Category     string `json:"category"`
	PaidAt       string `json:"paid_at"`
	Note         string `json:"note"`
	ImagePath    string `json:"image_path"`
	ContentHash  string `json:"content_hash"`
	DedupKey     string `json:"dedup_key"`
	RawModelJSON string `json:"raw_model_json,omitempty"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
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
