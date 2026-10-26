package model

import (
	"fmt"
	"strings"
	"time"
)

// ParseRange interprets from/to query bounds.
//
// A YYYY-MM-DD value is a calendar day in loc: from is inclusive at local
// midnight, to is inclusive through the end of that local day.
// An RFC3339 value is an exact instant: from is inclusive, to is inclusive
// (the exclusive upper bound is one second later, matching the second
// precision we store).
// Empty strings mean "no bound".
func ParseRange(from, to string, loc *time.Location) (start *time.Time, endExclusive *time.Time, err error) {
	if loc == nil {
		loc = time.UTC
	}
	if from != "" {
		t, dateOnly, err := parseBound(from, loc)
		if err != nil {
			return nil, nil, fmt.Errorf("from: %w", err)
		}
		if dateOnly {
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
		}
		u := t.UTC()
		start = &u
	}
	if to != "" {
		t, dateOnly, err := parseBound(to, loc)
		if err != nil {
			return nil, nil, fmt.Errorf("to: %w", err)
		}
		var u time.Time
		if dateOnly {
			u = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1).UTC()
		} else {
			u = t.UTC().Add(time.Second)
		}
		endExclusive = &u
	}
	if start != nil && endExclusive != nil && !start.Before(*endExclusive) {
		return nil, nil, fmt.Errorf("from must be before or equal to to")
	}
	return start, endExclusive, nil
}

// ResolveQuery parses the filters shared by the HTTP API and the MCP tools.
// limitSet is false when the caller omitted limit; 0 then means the default.
func ResolveQuery(from, to, category string, limit int, limitSet bool, loc *time.Location) (start *time.Time, endExclusive *time.Time, cat string, lim int, err error) {
	start, endExclusive, err = ParseRange(strings.TrimSpace(from), strings.TrimSpace(to), loc)
	if err != nil {
		return nil, nil, "", 0, err
	}
	lim, err = NormalizeLimit(limit, limitSet)
	if err != nil {
		return nil, nil, "", 0, err
	}
	return start, endExclusive, strings.TrimSpace(category), lim, nil
}

func parseBound(s string, loc *time.Location) (time.Time, bool, error) {
	if len(s) == len("2006-01-02") && s[4] == '-' && s[7] == '-' {
		t, err := time.ParseInLocation("2006-01-02", s, loc)
		if err != nil {
			return time.Time{}, false, fmt.Errorf("want YYYY-MM-DD or RFC3339, got %q", s)
		}
		return t, true, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, false, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, false, nil
	}
	return time.Time{}, false, fmt.Errorf("want YYYY-MM-DD or RFC3339, got %q", s)
}
