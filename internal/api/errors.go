package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Oasis1i/bill-snap/internal/extract"
)

// apiError is the JSON body of every failed HTTP call.
// Error is a stable machine code. Message is one short Chinese sentence for a
// phone notification: no stack traces, secrets, or provider payloads.
type apiError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

const (
	msgUnauthorized       = "未授权，请检查上传令牌。"
	msgImageTooLarge      = "图片超过上传大小限制。"
	msgInvalidForm        = "上传格式不对，请重新选择图片。"
	msgMissingImage       = "缺少图片，请用表单字段 image 上传。"
	msgUnreadableImage    = "图片读取失败，请重试。"
	msgEmptyImage         = "图片是空的。"
	msgBadImageType       = "只支持 png、jpeg 或 webp。"
	msgExtractTimeout     = "识别超时，请稍后重试。"
	msgExtractBadResponse = "识别结果无法解析，请重试。"
	msgExtractProvider    = "识别服务出错，请稍后重试。"
	msgExtractNotConfig   = "识别服务未配置。"
	msgExtractFailed      = "识别失败，请稍后重试。"
	msgMissingPaidAt      = "没识别出支付时间。"
	msgNegativeAmount     = "金额不能为负数。"
	msgStoreRead          = "读取账单失败，请稍后重试。"
	msgImageSave          = "保存图片失败，请稍后重试。"
	msgStoreWrite         = "保存账单失败，请稍后重试。"
	msgInternal           = "服务器出错，请稍后重试。"
	msgBadLimit           = "条数应为 1 到 500 的整数。"
	msgBadRange           = "开始时间不能晚于结束时间。"
	msgBadDate            = "日期格式应为 YYYY-MM-DD 或 RFC3339。"
	msgBadQuery           = "查询参数不正确。"
	msgListFailed         = "查询账单失败，请稍后重试。"
	msgSummaryFailed      = "汇总失败，请稍后重试。"
	msgNotABill           = "这张图不是付款账单。"
)

// Built-in mock notes are English. The phone reads reason, so map those
// exact strings. Any other non-empty note is the model's own text.
var discardReasonZH = map[string]string{
	"empty image": "图片是空的。",
	"image is not a decodable png, jpeg, or webp":                       "图片无法解码，未当作账单。",
	"image is smaller than 64x64":                                       "图片太小，未当作账单。",
	"image is nearly a solid color, so it was not treated as a receipt": "图片几乎是纯色，未当作账单。",
}

const reasonRunes = 80

var (
	bearerRE = regexp.MustCompile(`(?i)bearer\s+\S{6,}`)
	keyRE    = regexp.MustCompile(`\bsk-[A-Za-z0-9]{8,}\b`)
)

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiError{Error: code, Message: message})
}

func writeExtractFailure(w http.ResponseWriter, err error) {
	status, code, message := classifyExtract(err)
	slog.Error("extract", "code", code, "detail", logDetail(err))
	writeError(w, status, code, message)
}

func classifyExtract(err error) (status int, code, message string) {
	var f *extract.Failure
	if errors.As(err, &f) {
		switch f.Kind {
		case extract.FailureTimeout:
			return http.StatusBadGateway, "extract_timeout", msgExtractTimeout
		case extract.FailureBadResponse:
			return http.StatusBadGateway, "extract_bad_response", msgExtractBadResponse
		case extract.FailureProvider:
			return http.StatusBadGateway, "extract_provider_error", msgExtractProvider
		case extract.FailureNotConfigured:
			return http.StatusBadGateway, "extract_not_configured", msgExtractNotConfig
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusBadGateway, "extract_timeout", msgExtractTimeout
	}
	return http.StatusBadGateway, "extract_failed", msgExtractFailed
}

func writeQueryError(w http.ResponseWriter, err error) {
	code, message := "bad_query", msgBadQuery
	text := ""
	if err != nil {
		text = err.Error()
	}
	switch {
	case strings.Contains(text, "limit"):
		code, message = "bad_limit", msgBadLimit
	case strings.Contains(text, "from must be before"):
		code, message = "bad_range", msgBadRange
	case strings.HasPrefix(text, "from:"), strings.HasPrefix(text, "to:"), strings.Contains(text, "YYYY-MM-DD"):
		code, message = "bad_date", msgBadDate
	}
	writeError(w, http.StatusBadRequest, code, message)
}

// notBillReason is the phone-facing sentence when is_bill is false.
// Empty notes become a Chinese sentence. Known mock notes are translated.
// Anything else is the model's note, collapsed to one short line.
func notBillReason(notes string) string {
	notes = strings.TrimSpace(notes)
	if zh, ok := discardReasonZH[notes]; ok {
		return zh
	}
	if notes == "" {
		return msgNotABill
	}
	notes = strings.Join(strings.Fields(notes), " ")
	notes = redactSecrets(notes)
	if utf8.RuneCountInString(notes) <= reasonRunes {
		return notes
	}
	return string([]rune(notes)[:reasonRunes]) + "…"
}

func logDetail(err error) string {
	detail := ""
	var f *extract.Failure
	if errors.As(err, &f) {
		detail = f.Detail
	}
	if detail == "" && err != nil {
		detail = err.Error()
	}
	return clipRunes(redactSecrets(detail), 200)
}

func redactSecrets(s string) string {
	s = bearerRE.ReplaceAllString(s, "Bearer [redacted]")
	s = keyRE.ReplaceAllString(s, "[redacted]")
	return s
}

func clipRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || s == "" {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "..."
}
