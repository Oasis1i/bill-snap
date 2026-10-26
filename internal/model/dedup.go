package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// PaidAtDefaultedNote is appended to a bill note when the model omits paid_at
// and the server fills the upload time. Startup backfill looks for this exact
// sentence on rows written before paid_at_defaulted existed.
const PaidAtDefaultedNote = "paid_at missing; defaulted to upload time"

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

// NormalizeOrderNo is the identity used for order-number dedup.
// An empty result means the screenshot had no order or transaction number.
//
// The value is trimmed, internal whitespace and common separators are removed,
// fullwidth ASCII digits and letters are folded, and ASCII letters are
// uppercased. Placeholders such as 无, 未知, none, and n/a become empty.
func NormalizeOrderNo(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		if unicode.IsSpace(r) {
			continue
		}
		switch r {
		case '-', '_', '—', '–', '－', ':', '：', '#', '＃', '/', '／', '.', '。':
			continue
		}
		b.WriteRune(foldOrderRune(r))
	}
	out := b.String()
	switch out {
	case "", "无", "未知", "暂无", "空", "NONE", "NULL", "NA", "NIL", "UNDEFINED", "无订单号", "无交易单号":
		return ""
	}
	return out
}

func foldOrderRune(r rune) rune {
	switch {
	case r >= '０' && r <= '９':
		return '0' + (r - '０')
	case r >= 'Ａ' && r <= 'Ｚ':
		return 'A' + (r - 'Ａ')
	case r >= 'ａ' && r <= 'ｚ':
		return 'A' + (r - 'ａ')
	default:
		return unicode.ToUpper(r)
	}
}

// SameLocalDay reports whether a and b fall on the same calendar date in loc.
// A nil loc is treated as UTC.
func SameLocalDay(a, b time.Time, loc *time.Location) bool {
	if loc == nil {
		loc = time.UTC
	}
	aa := a.In(loc)
	bb := b.In(loc)
	return aa.Year() == bb.Year() && aa.Month() == bb.Month() && aa.Day() == bb.Day()
}

// MakeDedupKey identifies one payment across different screenshots.
//
// Material, UTF-8, joined by "|":
//
//	"<amount_cents>|<CURRENCY>|<normalized merchant>|<paid_at RFC3339 minute>"
//
// The key is the hex SHA-256 of that string. Same screenshot bytes are also
// deduped separately via content_hash. A visible order number is checked
// before this key, and a defaulted paid_at can also match the same merchant
// and amount on the same local day. This key still catches a second photo
// with the same amount, merchant, and minute.
func MakeDedupKey(amountCents int64, currency, merchant string, paidAt time.Time) string {
	return hashMaterial(dedupMaterial(amountCents, currency, merchant, paidAt))
}

// MakeDistinctOrderDedupKey is MakeDedupKey's material plus "|" and the
// normalized order number. The store uses it only when the plain dedup_key is
// already stored for a different order number, so both payments can exist
// without changing the key of rows that have no order number.
func MakeDistinctOrderDedupKey(amountCents int64, currency, merchant string, paidAt time.Time, orderNo string) string {
	return hashMaterial(dedupMaterial(amountCents, currency, merchant, paidAt) + "|" + NormalizeOrderNo(orderNo))
}

func dedupMaterial(amountCents int64, currency, merchant string, paidAt time.Time) string {
	cur := strings.ToUpper(strings.TrimSpace(currency))
	if cur == "" {
		cur = "CNY"
	}
	paid := NormalizePaidAt(paidAt).Format(time.RFC3339)
	return fmt.Sprintf("%d|%s|%s|%s", amountCents, cur, NormalizeMerchant(merchant), paid)
}

func hashMaterial(material string) string {
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:])
}
