package extract

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"unicode/utf8"
)

// FailureKind is the machine classification of a vision failure.
// Callers map it to a short user-facing sentence. Detail is for logs only.
type FailureKind string

const (
	FailureTimeout       FailureKind = "timeout"
	FailureBadResponse   FailureKind = "bad_response"
	FailureProvider      FailureKind = "provider"
	FailureNotConfigured FailureKind = "not_configured"
)

// logDetailRunes caps provider text stored for logs.
const logDetailRunes = 200

var (
	bearerRE = regexp.MustCompile(`(?i)bearer\s+\S{6,}`)
	keyRE    = regexp.MustCompile(`\bsk-[A-Za-z0-9]{8,}\b`)
)

// Failure is a classified extractor error.
// Error() is a short label with no provider payload. Detail is truncated,
// redacted, and meant for logs — not for an HTTP response.
type Failure struct {
	Kind     FailureKind
	Provider string
	Detail   string
	Err      error
}

func (f *Failure) Error() string {
	if f == nil {
		return "extract failed"
	}
	kind := string(f.Kind)
	if kind == "" {
		kind = "failed"
	}
	if f.Provider != "" {
		return f.Provider + ": " + kind
	}
	return kind
}

func (f *Failure) Unwrap() error { return f.Err }

func failure(kind FailureKind, provider, detail, secret string, err error) *Failure {
	return &Failure{
		Kind:     kind,
		Provider: provider,
		Detail:   clip(redact(detail, secret), logDetailRunes),
		Err:      err,
	}
}

func transportFailure(provider string, err error) *Failure {
	kind := FailureProvider
	if isTimeout(err) {
		kind = FailureTimeout
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return failure(kind, provider, msg, "", err)
}

func providerStatus(provider, secret string, status int, body []byte) *Failure {
	return failure(FailureProvider, provider, fmt.Sprintf("HTTP %d: %s", status, string(body)), secret, nil)
}

func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

func redact(s, secret string) string {
	if len(secret) >= 8 {
		s = strings.ReplaceAll(s, secret, "[redacted]")
	}
	s = bearerRE.ReplaceAllString(s, "Bearer [redacted]")
	s = keyRE.ReplaceAllString(s, "[redacted]")
	return s
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "..."
}
