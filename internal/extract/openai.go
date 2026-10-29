package extract

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Oasis1i/bill-snap/internal/model"
	"github.com/Oasis1i/bill-snap/internal/version"
)

// visionPrompt asks for one JSON object. Vendor models sometimes wrap it in
// markdown; the parser strips a single fenced block. category is not a free
// label: the schema enum is the effective category list, and unsure output must be 其他.
func visionPrompt() string {
	schema, err := json.Marshal(struct {
		Type string   `json:"type"`
		Enum []string `json:"enum"`
	}{Type: "string", Enum: model.Categories()})
	if err != nil {
		// Categories is a fixed []string; marshal cannot fail.
		schema = []byte(`{"type":"string","enum":["其他"]}`)
	}
	return `You extract payment receipts from screenshots for a personal expense tracker.
Reply with one JSON object and no markdown.
Schema:
{"is_bill":true,"amount":"12.34","currency":"CNY","merchant":"","category":"外卖","paid_at":"2026-10-26T12:30:00+08:00","order_no":"","confidence":0.0,"notes":""}
Rules:
- is_bill is false when the image is not a payment receipt (chat, landscape, home screen, blank image). Leave amount empty in that case.
- amount is major currency units, not cents. Example: 18.5 yuan is "18.50".
- paid_at is RFC3339 with a numeric offset when the receipt shows a time. Use YYYY-MM-DD when only a date is visible. Leave paid_at empty when no date or time is visible. Do not invent a time.
- order_no is the payment's order or transaction number when one is visible (订单号, 交易单号, 商户单号, 订单编号, 交易号, 商户订单号). If several are visible, prefer 交易单号, then 订单号, then 商户单号. Copy the number as printed. Leave order_no empty when none is visible. Do not invent one.
- category must match this schema exactly: ` + string(schema) + `. If you are unsure which label fits, use ` + model.CategoryOther + `. Do not invent a label, do not translate it, and do not return a close variant.
- confidence is from 0 to 1.`
}

// openAICompat is a thin client for 通义 VL, 豆包, and 智谱 GLM-4V style
// OpenAI-compatible vision endpoints. It is not wired to a chatbot subscription.
type openAICompat struct {
	name    string
	apiKey  string
	baseURL string
	model   string
	loc     *time.Location
	client  *http.Client
}

func (o *openAICompat) Extract(ctx context.Context, imageBytes []byte, mime string) (Result, error) {
	if strings.TrimSpace(o.apiKey) == "" {
		return Result{}, fmt.Errorf("%s: VISION_API_KEY is empty; fill API details before use", o.name)
	}
	if strings.TrimSpace(o.model) == "" {
		return Result{}, fmt.Errorf("%s: VISION_MODEL is empty; fill API details before use", o.name)
	}
	if mime == "" {
		mime = "application/octet-stream"
	}
	payload := chatRequest{
		Model: o.model,
		Messages: []chatMessage{{
			Role: "user",
			Content: []chatPart{
				{Type: "text", Text: visionPrompt()},
				{Type: "image_url", ImageURL: &chatImageURL{
					URL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(imageBytes),
				}},
			},
		}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, err
	}
	endpoint := strings.TrimRight(o.baseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "bill-snap/"+version.Version)

	resp, err := o.client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("%s: request failed: %w", o.name, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return Result{}, fmt.Errorf("%s: read response: %w", o.name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("%s: HTTP %d: %s", o.name, resp.StatusCode, truncate(string(respBody), 300))
	}
	var parsed chatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return Result{}, fmt.Errorf("%s: decode response: %w", o.name, err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return Result{}, fmt.Errorf("%s: %s", o.name, parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return Result{}, fmt.Errorf("%s: response had no choices", o.name)
	}
	text := contentString(parsed.Choices[0].Message.Content)
	raw, err := extractJSONObject(text)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", o.name, err)
	}
	return resultFromModelJSON(o.name, raw, o.loc)
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role    string     `json:"role"`
	Content []chatPart `json:"content"`
}

type chatPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *chatImageURL `json:"image_url,omitempty"`
}

type chatImageURL struct {
	URL string `json:"url"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type visionPayload struct {
	IsBill          bool            `json:"is_bill"`
	Amount          json.RawMessage `json:"amount"`
	Currency        string          `json:"currency"`
	Merchant        string          `json:"merchant"`
	Category        flexCategory    `json:"category"`
	PaidAt          string          `json:"paid_at"`
	OrderNo         flexString      `json:"order_no"`
	TransactionNo   flexString      `json:"transaction_no"`
	TransactionID   flexString      `json:"transaction_id"`
	MerchantOrderNo flexString      `json:"merchant_order_no"`
	OutTradeNo      flexString      `json:"out_trade_no"`
	OrderNumber     flexString      `json:"order_number"`
	OrderNoCN       flexString      `json:"订单号"`
	TxnNoCN         flexString      `json:"交易单号"`
	MerchantNoCN    flexString      `json:"商户单号"`
	MerchantOrderCN flexString      `json:"商户订单号"`
	OrderCodeCN     flexString      `json:"订单编号"`
	TradeNoCN       flexString      `json:"交易号"`
	Confidence      float64         `json:"confidence"`
	Notes           string          `json:"notes"`
}

// flexCategory accepts a JSON string. Null and non-strings become empty so
// NormalizeCategory can map them to 其他 instead of failing the upload.
type flexCategory string

func (c *flexCategory) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*c = ""
		return nil
	}
	if data[0] != '"' {
		*c = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	*c = flexCategory(s)
	return nil
}

// flexString accepts a JSON string or number. Receipt models sometimes emit
// an all-digit order number without quotes.
type flexString string

func (f *flexString) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*f = ""
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*f = flexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		return err
	}
	*f = flexString(n.String())
	return nil
}

func contentString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return string(raw)
}

func extractJSONObject(s string) (json.RawMessage, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```JSON")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	s = strings.TrimSpace(s)
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end < start {
		return nil, fmt.Errorf("model response did not contain a JSON object")
	}
	raw := json.RawMessage(s[start : end+1])
	if !json.Valid(raw) {
		return nil, fmt.Errorf("model response JSON is invalid")
	}
	return raw, nil
}

func resultFromModelJSON(provider string, raw json.RawMessage, loc *time.Location) (Result, error) {
	var payload visionPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Result{}, fmt.Errorf("%s: %w", provider, err)
	}
	notes := strings.TrimSpace(payload.Notes)
	if !payload.IsBill {
		return Result{
			IsBill:     false,
			Currency:   strings.ToUpper(strings.TrimSpace(payload.Currency)),
			Merchant:   strings.TrimSpace(payload.Merchant),
			Category:   model.NormalizeCategory(string(payload.Category)),
			Confidence: payload.Confidence,
			Notes:      notes,
			RawJSON:    raw,
		}, nil
	}
	amountText, err := rawAmount(payload.Amount)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", provider, err)
	}
	if strings.TrimSpace(amountText) == "" {
		return Result{}, fmt.Errorf("%s: model marked is_bill but amount is empty", provider)
	}
	cents, err := model.ParseAmountCents(amountText)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", provider, err)
	}
	paid, defaulted, err := parseModelTime(payload.PaidAt, loc)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", provider, err)
	}
	if defaulted {
		if notes != "" {
			notes += "; "
		}
		notes += model.PaidAtDefaultedNote
	}
	currency := strings.ToUpper(strings.TrimSpace(payload.Currency))
	if currency == "" {
		currency = "CNY"
	}
	return Result{
		IsBill:          true,
		Amount:          model.FormatCents(cents),
		AmountCents:     cents,
		Currency:        currency,
		Merchant:        strings.TrimSpace(payload.Merchant),
		Category:        model.NormalizeCategory(string(payload.Category)),
		PaidAt:          paid,
		PaidAtDefaulted: defaulted,
		OrderNo:         firstOrderNo(payload),
		Confidence:      payload.Confidence,
		Notes:           notes,
		RawJSON:         raw,
	}, nil
}

func firstOrderNo(p visionPayload) string {
	for _, v := range []flexString{
		p.OrderNo,
		p.TransactionNo,
		p.TransactionID,
		p.MerchantOrderNo,
		p.OutTradeNo,
		p.OrderNumber,
		p.TxnNoCN,
		p.OrderNoCN,
		p.MerchantNoCN,
		p.MerchantOrderCN,
		p.OrderCodeCN,
		p.TradeNoCN,
	} {
		if n := model.NormalizeOrderNo(string(v)); n != "" {
			return n
		}
	}
	return ""
}

func rawAmount(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var n json.Number
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&n); err != nil {
		return "", fmt.Errorf("amount is not a string or number")
	}
	return n.String(), nil
}

func parseModelTime(s string, loc *time.Location) (time.Time, bool, error) {
	if loc == nil {
		loc = time.UTC
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Now().UTC(), true, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, false, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, false, nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, loc); err == nil {
		return t, false, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, loc); err == nil {
		return time.Date(t.Year(), t.Month(), t.Day(), 12, 0, 0, 0, loc), false, nil
	}
	return time.Time{}, false, fmt.Errorf("unrecognized paid_at %q", s)
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
