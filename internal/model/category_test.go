package model

import (
	"strings"
	"testing"
)

func TestCategoriesClosedSet(t *testing.T) {
	want := []string{"外卖", "餐饮", "交通", "日用", "购物", "房租", "工资", "娱乐", "医疗", "其他"}
	if len(Categories) != len(want) {
		t.Fatalf("len %d", len(Categories))
	}
	seen := map[string]struct{}{}
	for i, c := range Categories {
		if c != want[i] {
			t.Fatalf("index %d: got %q want %q", i, c, want[i])
		}
		if c == "" || strings.TrimSpace(c) != c {
			t.Fatalf("category %q is not a trimmed exact label", c)
		}
		if _, ok := seen[c]; ok {
			t.Fatalf("duplicate %q", c)
		}
		seen[c] = struct{}{}
		if got := NormalizeCategory(c); got != c {
			t.Fatalf("round trip %q -> %q", c, got)
		}
		if _, ok := categorySet[c]; !ok {
			t.Fatalf("categorySet missing %q", c)
		}
	}
	if len(categorySet) != len(Categories) {
		t.Fatalf("categorySet len %d", len(categorySet))
	}
	if CategoryOther != "其他" {
		t.Fatalf("CategoryOther %q", CategoryOther)
	}
}

func TestNormalizeCategory(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"外卖", "外卖"},
		{" 外卖 ", "外卖"},
		{"\n其他\t", "其他"},
		{"\u3000房租\u3000", "房租"},
		{"", "其他"},
		{"   ", "其他"},
		{"外卖配送", "其他"},
		{"外 卖", "其他"},
		{"waimai", "其他"},
		{"Takeout", "其他"},
		{"餐饮美食", "其他"},
		{"其他类", "其他"},
		{"购物.", "其他"},
	}
	for _, tc := range cases {
		if got := NormalizeCategory(tc.in); got != tc.want {
			t.Fatalf("%q: got %q want %q", tc.in, got, tc.want)
		}
	}
}
