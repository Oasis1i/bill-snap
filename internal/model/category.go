package model

import (
	"fmt"
	"strings"
	"sync"
)

// CategoryOther is the fallback when a model leaves category empty or
// returns something outside the effective category list. It is always
// kept in that list.
const CategoryOther = "其他"

// defaultCategories is the built-in list. Config may replace it or edit it
// with +label / -label. Order is the order shown to the model.
var defaultCategories = []string{
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

type categoryToken struct {
	op    byte // 0 = bare label, '+' add, '-' remove
	label string
}

var (
	categoryMu  sync.RWMutex
	categories  []string
	categorySet map[string]struct{}
)

func init() {
	UseCategories(defaultCategories)
}

// DefaultCategories returns a copy of the built-in list. It does not follow
// CATEGORIES. Callers may modify the returned slice.
func DefaultCategories() []string {
	out := make([]string, len(defaultCategories))
	copy(out, defaultCategories)
	return out
}

// Categories returns a copy of the effective list for this process.
// It is the built-in defaults until UseCategories replaces it.
func Categories() []string {
	categoryMu.RLock()
	defer categoryMu.RUnlock()
	out := make([]string, len(categories))
	copy(out, categories)
	return out
}

// UseCategories installs list as the effective category set.
// Empty labels are dropped, duplicates keep the first occurrence, and
// 其他 is appended when missing. The caller's slice is not retained.
func UseCategories(list []string) {
	cleaned := dedupeCategories(list)
	cleaned = ensureOther(cleaned)
	set := make(map[string]struct{}, len(cleaned))
	for _, c := range cleaned {
		set[c] = struct{}{}
	}
	categoryMu.Lock()
	categories = cleaned
	categorySet = set
	categoryMu.Unlock()
}

// ResolveCategories interprets the CATEGORIES setting.
//
// An empty string returns the built-in defaults.
// A comma-separated list (also 中文逗号 and 顿号) replaces those defaults.
// A list made only of +label and -label edits the defaults: + appends a new
// label, - removes a built-in one. Do not mix the two forms.
// 其他 is always present. -其他 is an error.
//
// Close variants are not folded into an existing label. "外卖配送" is its
// own label only when it is actually configured.
func ResolveCategories(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultCategories(), nil
	}
	tokens, err := splitCategoryTokens(raw)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("CATEGORIES has no labels")
	}
	patched, plain := false, false
	for _, tok := range tokens {
		if tok.op != 0 {
			patched = true
		} else {
			plain = true
		}
	}
	if patched && plain {
		return nil, fmt.Errorf("CATEGORIES mixes a full list with +/− edits; set the full list, or only +label and -label items")
	}
	if !patched {
		labels := make([]string, len(tokens))
		for i, tok := range tokens {
			labels[i] = tok.label
		}
		return ensureOther(dedupeCategories(labels)), nil
	}
	return applyCategoryPatch(tokens)
}

func applyCategoryPatch(tokens []categoryToken) ([]string, error) {
	base := DefaultCategories()
	for _, tok := range tokens {
		if tok.label == CategoryOther && tok.op == '-' {
			return nil, fmt.Errorf("CATEGORIES: %s cannot be removed", CategoryOther)
		}
		switch tok.op {
		case '+':
			if !categoryContains(base, tok.label) {
				base = append(base, tok.label)
			}
		case '-':
			idx := categoryIndex(base, tok.label)
			if idx < 0 {
				return nil, fmt.Errorf("CATEGORIES: cannot remove %q; it is not in the current list", tok.label)
			}
			base = append(base[:idx], base[idx+1:]...)
		default:
			return nil, fmt.Errorf("CATEGORIES: invalid edit %q", tok.label)
		}
	}
	return ensureOther(base), nil
}

func splitCategoryTokens(raw string) ([]categoryToken, error) {
	raw = strings.NewReplacer("，", ",", "、", ",", "\n", ",").Replace(raw)
	parts := strings.Split(raw, ",")
	out := make([]categoryToken, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		tok := categoryToken{label: part}
		if part[0] == '+' || part[0] == '-' {
			tok.op = part[0]
			tok.label = strings.TrimSpace(part[1:])
			if tok.label == "" {
				return nil, fmt.Errorf("CATEGORIES: empty label in %q", part)
			}
			if tok.label[0] == '+' || tok.label[0] == '-' {
				return nil, fmt.Errorf("CATEGORIES: label %q has more than one +/- prefix", part)
			}
		}
		out = append(out, tok)
	}
	return out, nil
}

func dedupeCategories(list []string) []string {
	seen := make(map[string]struct{}, len(list))
	out := make([]string, 0, len(list))
	for _, c := range list {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out
}

func ensureOther(list []string) []string {
	if categoryContains(list, CategoryOther) {
		return list
	}
	return append(list, CategoryOther)
}

func categoryContains(list []string, label string) bool {
	return categoryIndex(list, label) >= 0
}

func categoryIndex(list []string, label string) int {
	for i, c := range list {
		if c == label {
			return i
		}
	}
	return -1
}

// NormalizeCategory trims whitespace and returns c when it is an exact member
// of the effective category list. Empty and unknown values, including close
// variants, become CategoryOther. The original text is not kept as a free-form label.
func NormalizeCategory(c string) string {
	c = strings.TrimSpace(c)
	categoryMu.RLock()
	_, ok := categorySet[c]
	categoryMu.RUnlock()
	if ok {
		return c
	}
	return CategoryOther
}
