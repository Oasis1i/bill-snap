package model

import (
	"fmt"
	"math/big"
	"strings"
)

// FormatCents renders minor units as a major-unit decimal, e.g. 1850 -> "18.50".
func FormatCents(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}

// ParseAmountCents parses a major-unit amount into minor units.
// Accepts "18.50", "18", "¥18.50", "CNY 18.50", and thousands separators.
// More than two decimal places are rounded half away from zero.
// Negative amounts are rejected.
func ParseAmountCents(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "￥")
	s = strings.TrimPrefix(s, "¥")
	s = strings.TrimSpace(s)
	if len(s) >= 3 && strings.EqualFold(s[:3], "cny") {
		s = strings.TrimSpace(s[3:])
	}
	s = strings.ReplaceAll(s, ",", "")
	if s == "" {
		return 0, fmt.Errorf("empty amount")
	}
	neg := false
	if strings.HasPrefix(s, "+") {
		s = strings.TrimSpace(s[1:])
	}
	if strings.HasPrefix(s, "-") {
		neg = true
		s = strings.TrimSpace(s[1:])
	}
	r := new(big.Rat)
	if _, ok := r.SetString(s); !ok {
		return 0, fmt.Errorf("invalid amount %q", raw)
	}
	if r.Sign() < 0 {
		neg = true
		r.Abs(r)
	}
	r.Mul(r, big.NewRat(100, 1))
	num := new(big.Int).Set(r.Num())
	den := new(big.Int).Set(r.Denom())
	cents := new(big.Int).Quo(num, den)
	rem := new(big.Int).Rem(num, den)
	if rem.Sign() != 0 {
		twice := new(big.Int).Lsh(rem, 1)
		if twice.Cmp(den) >= 0 {
			cents.Add(cents, big.NewInt(1))
		}
	}
	if !cents.IsInt64() {
		return 0, fmt.Errorf("amount out of range")
	}
	if neg {
		return 0, fmt.Errorf("negative amount")
	}
	return cents.Int64(), nil
}

// NormalizeLimit applies the list cap. When the caller omitted limit, n is 0
// and provided is false, and the default is 100. Explicit values must be 1..500.
func NormalizeLimit(n int, provided bool) (int, error) {
	if !provided || n == 0 {
		return 100, nil
	}
	if n < 1 || n > 500 {
		return 0, fmt.Errorf("limit must be between 1 and 500")
	}
	return n, nil
}
