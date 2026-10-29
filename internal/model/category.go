package model

import "strings"

// CategoryOther is the fallback when a model leaves category empty or
// returns something outside Categories.
const CategoryOther = "其他"

// Categories is the only set of bill categories. Vision output, the mock
// extractor, and stored rows use these exact strings. Order is stable and
// is the order shown to the model.
//
// Do not accept translations or close variants (外卖配送, 外 卖, waimai).
// Those are not members of this list.
var Categories = []string{
	"外卖",
	"餐饮",
	"交通",
	"日用",
	"购物",
	"房租",
	"工资",
	"娱乐",
	"医疗",
	CategoryOther,
}

// categorySet is the lookup form of Categories.
var categorySet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(Categories))
	for _, c := range Categories {
		m[c] = struct{}{}
	}
	return m
}()

// NormalizeCategory trims whitespace and returns c when it is an exact member
// of Categories. Empty and unknown values, including close variants, become
// CategoryOther. The original text is not kept as a free-form label.
func NormalizeCategory(c string) string {
	c = strings.TrimSpace(c)
	if _, ok := categorySet[c]; ok {
		return c
	}
	return CategoryOther
}
