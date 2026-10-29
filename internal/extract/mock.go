package extract

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"strings"
	"time"

	"github.com/Oasis1i/bill-snap/internal/model"
	"golang.org/x/image/webp"
)

// stddevFloor is the 8-bit luminance standard deviation below which an image
// is treated as blank. Solid colors and near-solid screenshots fall under it;
// a photo or a strong gradient sits well above it.
const stddevFloor = 12.0

const minBillSide = 64

// catalog is the fixed demo set. The mock picks an entry from the image hash,
// so the same bytes always produce the same merchant and category.
// Each category is a member of model.Categories; the extractor does not
// invent a label.
var catalog = []struct {
	Merchant string
	Category string
}{
	{"美团外卖", "外卖"},
	{"饿了么", "外卖"},
	{"瑞幸咖啡", "餐饮"},
	{"星巴克", "餐饮"},
	{"滴滴出行", "交通"},
	{"盒马鲜生", "日用"},
	{"淘宝", "购物"},
}

// Mock is an offline extractor. Decision rules, in order:
//
//  1. Empty bytes, or bytes that are not a decodable png/jpeg/webp: IsBill false.
//  2. Width or height under 64 pixels: IsBill false.
//  3. Sampled 8-bit luminance standard deviation under 12: IsBill false
//     (blank or nearly solid images, including a plain white screenshot).
//  4. Otherwise IsBill true. Amount, merchant, category, order number, and
//     paid_at are a pure function of SHA-256(image bytes):
//     - amount_cents = 100 * (1 + hash[1] % 200)  → 1.00 .. 200.00 CNY
//     - merchant/category = catalog[hash[2] % len(catalog)]
//     - order_no = "M" + uppercase hex of hash[0:6] (unique per image)
//     - paid_at = 2026-01-01 12:00 UTC plus (hash[0] % 365) days
//
// The same file therefore always yields the same bill. Mock dates are not
// "today"; query a range that covers 2026, or use the paid_at from the upload.
// paid_at is always set, so PaidAtDefaulted is false.
type Mock struct{}

// NewMock returns the offline extractor.
func NewMock() *Mock { return &Mock{} }

// Extract implements Extractor.
func (m *Mock) Extract(ctx context.Context, imageBytes []byte, mime string) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	sum := sha256.Sum256(imageBytes)
	meta := map[string]any{
		"provider": "mock",
		"sha256":   hex.EncodeToString(sum[:]),
		"mime":     mime,
	}
	if len(imageBytes) == 0 {
		return notBill("empty image", meta), nil
	}
	img, err := decodeImage(imageBytes, mime)
	if err != nil {
		return notBill("image is not a decodable png, jpeg, or webp", meta), nil
	}
	bounds := img.Bounds()
	if bounds.Dx() < minBillSide || bounds.Dy() < minBillSide {
		return notBill("image is smaller than 64x64", meta), nil
	}
	sd := lumaStddev8(img)
	meta["luma_stddev"] = sd
	if sd < stddevFloor {
		return notBill("image is nearly a solid color, so it was not treated as a receipt", meta), nil
	}

	item := catalog[int(sum[2])%len(catalog)]
	cents := int64(sum[1]%200+1) * 100
	orderNo := "M" + strings.ToUpper(hex.EncodeToString(sum[:6]))
	// 2026 is not a leap year, so day offsets 0..364 stay inside the year.
	paid := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC).AddDate(0, 0, int(sum[0])%365)
	res := Result{
		IsBill:      true,
		Amount:      model.FormatCents(cents),
		AmountCents: cents,
		Currency:    "CNY",
		Merchant:    item.Merchant,
		Category:    model.NormalizeCategory(item.Category),
		PaidAt:      paid,
		OrderNo:     orderNo,
		Confidence:  0.99,
		Notes:       "mock extractor: deterministic fields from sha256",
	}
	meta["is_bill"] = true
	meta["amount"] = res.Amount
	meta["amount_cents"] = cents
	meta["currency"] = res.Currency
	meta["merchant"] = res.Merchant
	meta["category"] = res.Category
	meta["order_no"] = orderNo
	meta["paid_at"] = paid.Format(time.RFC3339)
	meta["paid_at_defaulted"] = false
	meta["confidence"] = res.Confidence
	meta["notes"] = res.Notes
	res.RawJSON = mustJSON(meta)
	return res, nil
}

func notBill(notes string, meta map[string]any) Result {
	if meta == nil {
		meta = map[string]any{}
	}
	meta["is_bill"] = false
	meta["notes"] = notes
	return Result{IsBill: false, Notes: notes, RawJSON: mustJSON(meta)}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"error":"marshal"}`)
	}
	return b
}

func decodeImage(data []byte, mime string) (image.Image, error) {
	switch mime {
	case "image/webp":
		return webp.Decode(bytes.NewReader(data))
	case "image/png":
		return png.Decode(bytes.NewReader(data))
	case "image/jpeg", "image/jpg":
		return jpeg.Decode(bytes.NewReader(data))
	default:
		if img, err := png.Decode(bytes.NewReader(data)); err == nil {
			return img, nil
		}
		if img, err := jpeg.Decode(bytes.NewReader(data)); err == nil {
			return img, nil
		}
		return webp.Decode(bytes.NewReader(data))
	}
}

func lumaStddev8(img image.Image) float64 {
	b := img.Bounds()
	stepX := b.Dx() / 40
	if stepX < 1 {
		stepX = 1
	}
	stepY := b.Dy() / 40
	if stepY < 1 {
		stepY = 1
	}
	var n, sum, sumsq float64
	for y := b.Min.Y; y < b.Max.Y; y += stepY {
		for x := b.Min.X; x < b.Max.X; x += stepX {
			r, g, bl, _ := img.At(x, y).RGBA()
			lum := (0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(bl)) / 257.0
			n++
			sum += lum
			sumsq += lum * lum
		}
	}
	if n == 0 {
		return 0
	}
	mean := sum / n
	variance := sumsq/n - mean*mean
	if variance < 0 {
		variance = 0
	}
	return math.Sqrt(variance)
}
