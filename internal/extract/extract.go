// Package extract turns a screenshot into structured bill fields.
//
// Mock is the default. It is deterministic and does not use the network.
// qwen (通义 VL), doubao (豆包 / 火山方舟), and glm (智谱 GLM-4V) are thin
// OpenAI-compatible HTTP clients behind the same Extractor interface. They
// compile and will POST a vision chat-completions request, but the prompt and
// vendor-specific parameters still need a real account before they are trustworthy.
// This package does not call a ChatGPT or Grok subscription.
package extract

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Extractor reads a screenshot and decides whether it is a payment bill.
type Extractor interface {
	Extract(ctx context.Context, imageBytes []byte, mime string) (Result, error)
}

// Result is the structured output of a vision pass.
// Amount is major currency units as a decimal string ("18.50").
// AmountCents is that value in minor units (fen, cents).
// OrderNo is the normalized order or transaction number, or empty.
// PaidAtDefaulted is true when the model omitted paid_at and PaidAt is the upload time.
type Result struct {
	IsBill          bool
	Amount          string
	AmountCents     int64
	Currency        string
	Merchant        string
	Category        string
	PaidAt          time.Time
	PaidAtDefaulted bool
	OrderNo         string
	Confidence      float64
	Notes           string
	RawJSON         json.RawMessage
}

// Options selects an implementation. An empty Provider means mock.
// HTTPClient is optional; remote providers otherwise use a 90s timeout.
type Options struct {
	Provider   string
	APIKey     string
	BaseURL    string
	Model      string
	Location   *time.Location
	HTTPClient *http.Client
}
