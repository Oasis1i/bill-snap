package model

import (
	"strings"
	"testing"
)

func TestDefaultCategories(t *testing.T) {
	UseCategories(DefaultCategories())
	want := []string{"外卖", "餐饮", "交通", "日用", "购物", "房租", "工资", "娱乐", "医疗", "其他"}
	got := DefaultCategories()
	if !sameStrings(got, want) {
		t.Fatalf("defaults %v", got)
	}
	got[0] = "mutated"
	if DefaultCategories()[0] != "外卖" || Categories()[0] != "外卖" {
		t.Fatal("default slice is shared with callers")
	}
	if Categories()[0] == "mutated" {
		t.Fatal("Categories follows a caller mutation")
	}
	for _, c := range want {
		if c == "" || strings.TrimSpace(c) != c {
			t.Fatalf("category %q is not a trimmed exact label", c)
		}
		if NormalizeCategory(c) != c {
			t.Fatalf("round trip %q", c)
		}
	}
	if CategoryOther != "其他" {
		t.Fatalf("CategoryOther %q", CategoryOther)
	}
}

func TestNormalizeCategoryDefaults(t *testing.T) {
	UseCategories(DefaultCategories())
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

func TestResolveCategories(t *testing.T) {
	defaults := DefaultCategories()
	cases := []struct {
		name    string
		raw     string
		want    []string
		wantErr string
	}{
		{name: "empty uses defaults", raw: "  ", want: defaults},
		{name: "full list appends 其他", raw: "宠物,订阅", want: []string{"宠物", "订阅", "其他"}},
		{name: "only other", raw: "其他", want: []string{"其他"}},
		{name: "full list keeps 其他 in place", raw: "其他, 外卖", want: []string{"其他", "外卖"}},
		{name: "full list trims and dedupes", raw: "外卖, 外卖,餐饮", want: []string{"外卖", "餐饮", "其他"}},
		{name: "chinese separators", raw: "宠物、订阅，其他", want: []string{"宠物", "订阅", "其他"}},
		{
			name: "extend and remove",
			raw:  "+宠物,+订阅,-工资",
			want: []string{"外卖", "餐饮", "交通", "日用", "购物", "房租", "娱乐", "医疗", "其他", "宠物", "订阅"},
		},
		{name: "add existing is a no-op", raw: "+外卖", want: defaults},
		{name: "add then remove", raw: "+宠物,-宠物", want: defaults},
		{name: "remove other", raw: "-其他", wantErr: "cannot be removed"},
		{name: "remove unknown", raw: "-咖啡", wantErr: "cannot remove"},
		{name: "mixed forms", raw: "宠物,+订阅", wantErr: "mixes"},
		{name: "empty edit", raw: "+", wantErr: "empty label"},
		{name: "only commas", raw: ",，、", wantErr: "no labels"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveCategories(tc.raw)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !sameStrings(got, tc.want) {
				t.Fatalf("got %v\nwant %v", got, tc.want)
			}
		})
	}
}

func TestNormalizeCategoryUsesConfiguredList(t *testing.T) {
	UseCategories([]string{"宠物", "外卖"})
	t.Cleanup(func() { UseCategories(DefaultCategories()) })

	if got := Categories(); !sameStrings(got, []string{"宠物", "外卖", "其他"}) {
		t.Fatalf("effective %v", got)
	}
	cases := []struct{ in, want string }{
		{"宠物", "宠物"},
		{" 外卖 ", "外卖"},
		{"工资", "其他"},
		{"外卖配送", "其他"},
		{"", "其他"},
		{"其他", "其他"},
	}
	for _, tc := range cases {
		if got := NormalizeCategory(tc.in); got != tc.want {
			t.Fatalf("%q: got %q want %q", tc.in, got, tc.want)
		}
	}
	// Caller mutation of the returned slice must not change the process list.
	snap := Categories()
	snap[0] = "nope"
	if Categories()[0] != "宠物" {
		t.Fatal("Categories returned its backing slice")
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
