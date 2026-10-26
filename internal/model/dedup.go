package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// NormalizePaidAt truncates a timestamp to the UTC minute.
// Dedup and the stored paid_at both use this precision so two shots of the
// same payment inside the same minute collapse to one row.
func NormalizePaidAt(t time.Time) time.Time {
	return t.UTC().Truncate(time.Minute)
}

// NormalizeMerchant trims and collapses whitespace, then lowercases.
// Case folding only affects ASCII; Chinese merchant names are unchanged.
func NormalizeMerchant(merchant string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(merchant)), " "))
}

// MakeDedupKey identifies one payment across different screenshots.
//
// Material, UTF-8, joined by "|":
//
//	"<amount_cents>|<CURRENCY>|<normalized merchant>|<paid_at RFC3339 minute>"
//
// The key is the hex SHA-256 of that string. Same screenshot bytes are also
// deduped separately via content_hash; this key catches a second photo of the
// same payment (same amount, merchant, and minute).
func MakeDedupKey(amountCents int64, currency, merchant string, paidAt time.Time) string {
	cur := strings.ToUpper(strings.TrimSpace(currency))
	if cur == "" {
		cur = "CNY"
	}
	paid := NormalizePaidAt(paidAt).Format(time.RFC3339)
	material := fmt.Sprintf("%d|%s|%s|%s", amountCents, cur, NormalizeMerchant(merchant), paid)
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:])
}
