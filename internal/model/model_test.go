package model

import (
	"testing"
	"time"
)

func TestParseAmountCents(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"18.50", 1850},
		{"18", 1800},
		{"18.5", 1850},
		{"¥18.50", 1850},
		{"￥18.50", 1850},
		{"CNY 1,234.56", 123456},
		{"cny18.00", 1800},
		{"0", 0},
		{"18.505", 1851},
	}
	for _, tc := range cases {
		got, err := ParseAmountCents(tc.in)
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("%q: got %d want %d", tc.in, got, tc.want)
		}
	}
	for _, bad := range []string{"", "abc", "-1", "¥-2"} {
		if _, err := ParseAmountCents(bad); err == nil {
			t.Fatalf("%q: expected error", bad)
		}
	}
}

func TestFormatCents(t *testing.T) {
	if got := FormatCents(1850); got != "18.50" {
		t.Fatal(got)
	}
	if got := FormatCents(5); got != "0.05" {
		t.Fatal(got)
	}
}

func TestDedupKey(t *testing.T) {
	a := time.Date(2026, 10, 26, 12, 0, 1, 0, time.UTC)
	b := time.Date(2026, 10, 26, 12, 0, 59, 0, time.UTC)
	c := time.Date(2026, 10, 26, 12, 1, 0, 0, time.UTC)
	k1 := MakeDedupKey(1850, "cny", "  美团外卖  ", a)
	k2 := MakeDedupKey(1850, "CNY", "美团外卖", b)
	k3 := MakeDedupKey(1850, "CNY", "美团外卖", c)
	if k1 != k2 {
		t.Fatalf("same minute should match\n%s\n%s", k1, k2)
	}
	if k1 == k3 {
		t.Fatal("next minute should differ")
	}
	if got := NormalizeMerchant("  Foo   BAR "); got != "foo bar" {
		t.Fatal(got)
	}
}

func TestParseRangeShanghaiDay(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	start, end, err := ParseRange("2026-10-26", "2026-10-26", loc)
	if err != nil {
		t.Fatal(err)
	}
	wantStart := time.Date(2026, 10, 25, 16, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 10, 26, 16, 0, 0, 0, time.UTC)
	if !start.Equal(wantStart) || !end.Equal(wantEnd) {
		t.Fatalf("got %s .. %s", start, end)
	}
	noon := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	evening := time.Date(2026, 10, 26, 18, 0, 0, 0, time.UTC)
	if noon.Before(*start) || !noon.Before(*end) {
		t.Fatal("noon UTC should fall inside the Shanghai day")
	}
	if evening.Before(*end) {
		t.Fatal("18:00Z is the next Shanghai day and must be excluded")
	}

	if _, _, err := ParseRange("2026-10-27", "2026-10-26", loc); err == nil {
		t.Fatal("expected reversed range error")
	}
	if _, _, err := ParseRange("yesterday", "", loc); err == nil {
		t.Fatal("expected bad from")
	}
}

func TestNormalizeLimit(t *testing.T) {
	n, err := NormalizeLimit(0, false)
	if err != nil || n != 100 {
		t.Fatalf("%d %v", n, err)
	}
	if _, err := NormalizeLimit(501, true); err == nil {
		t.Fatal("expected error")
	}
}
