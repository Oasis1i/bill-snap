package model

import "strings"

// MaxItemRunes is the stored length of a product-name list.
// Truncation counts runes, not bytes, so a Chinese name is not cut mid-character.
const MaxItemRunes = 100

// NormalizeItems trims, collapses whitespace, and keeps at most MaxItemRunes.
// An empty result means no product name was visible. Callers must not invent one.
func NormalizeItems(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= MaxItemRunes {
		return s
	}
	return strings.TrimSpace(string(r[:MaxItemRunes]))
}
