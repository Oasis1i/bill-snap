package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Oasis1i/bill-snap/internal/extract"
	"github.com/Oasis1i/bill-snap/internal/model"
)

// apiError is the JSON body of every failed HTTP call.
// Error is a stable machine code. Message is a short Chinese notification
// phrase: no clause after a Chinese comma, no full stop, no stack traces,
// secrets, or provider payloads.
type apiError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

const (
	msgUnauthorized       = "未授权"
	msgImageTooLarge      = "图片超过上传大小限制"
	msgInvalidForm        = "上传格式不对"
	msgMissingImage       = "缺少图片"
	msgUnreadableImage    = "图片读取失败"
	msgEmptyImage         = "图片是空的"
	msgBadImageType       = "图片格式不支持"
	msgExtractTimeout     = "识别超时"
	msgExtractBadResponse = "识别结果无法解析"
	msgExtractProvider    = "识别服务出错"
	msgExtractNotConfig   = "识别服务未配置"
	msgExtractFailed      = "识别失败"
	msgMissingPaidAt      = "没识别出支付时间"
	msgNegativeAmount     = "金额不能为负数"
	msgStoreRead          = "读取账单失败"
	msgImageSave          = "保存图片失败"
	msgStoreWrite         = "保存账单失败"
	msgInternal           = "服务器出错"
	msgBadLimit           = "条数应为 1 到 500 的整数"
	msgBadRange           = "开始时间不能晚于结束时间"
	msgBadDate            = "日期格式不对"
	msgBadQuery           = "查询参数不正确"
	msgListFailed         = "查询账单失败"
	msgSummaryFailed      = "汇总失败"
	msgNotABill           = "这张图不是付款账单"
)

// Built-in mock notes are English. The phone reads reason, so map those
// exact strings. Anything else is rewritten to short zh-Hans before it
// leaves the API. Model English is never copied through.
var discardReasonZH = map[string]string{
	"empty image": "图片是空的",
	"image is not a decodable png, jpeg, or webp":                       "图片无法解码",
	"image is smaller than 64x64":                                       "图片太小",
	"image is nearly a solid color, so it was not treated as a receipt": "图片几乎是纯色",
	"not a bill": "这张图不是付款账单",
}

const reasonRunes = 80

var (
	bearerRE     = regexp.MustCompile(`(?i)bearer\s+\S{6,}`)
	keyRE        = regexp.MustCompile(`\bsk-[A-Za-z0-9]{8,}\b`)
	asciiLetters = regexp.MustCompile(`[A-Za-z]`)
)

// englishReasonRules map casual model notes onto the same short phrases
// the phone already uses. The first matching rule wins.
var englishReasonRules = []struct {
	needles []string
	phrase  string
}{
	{[]string{"chat", "wechat", "weixin", "whatsapp", "imessage", "conversation"}, "聊天截图"},
	{[]string{"home screen", "homescreen", "lock screen", "launcher", "springboard"}, "主屏幕截图"},
	{[]string{"landscape", "scenery", "selfie"}, "风景照片"},
	{[]string{"solid color", "nearly a solid", "blank image", "blank screen", "white screen", "plain white", "empty image"}, "图片几乎是纯色"},
	{[]string{"too small", "smaller than", "64x64"}, "图片太小"},
	{[]string{"not a decodable", "cannot decode", "can't decode", "not an image", "invalid image"}, "图片无法解码"},
	{[]string{"not a bill", "not a receipt", "not a payment", "isn't a bill", "is not a bill", "screenshot"}, "这张图不是付款账单"},
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiError{Error: code, Message: notifyPhrase(message)})
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

// notBillReason is the phone-facing phrase when is_bill is false.
// It is always short zh-Hans: no English sentence, no clause after a comma,
// no full stop. Known mock notes and common English model notes are mapped.
// Chinese model notes are shortened. Anything else becomes the default phrase.
func notBillReason(notes string) string {
	notes = strings.TrimSpace(notes)
	if notes == "" {
		return msgNotABill
	}
	if zh, ok := discardReasonZH[reasonKey(notes)]; ok {
		return zh
	}
	if hasHan(notes) {
		if phrase := chinesePhrase(notes); phrase != "" {
			return clipPhrase(phrase)
		}
	}
	if zh, ok := matchEnglishReason(notes); ok {
		return zh
	}
	return msgNotABill
}

// phoneNote is the bill note shown to the phone. Storage may still hold the
// English paid-at marker so old rows can be backfilled. The response does not.
func phoneNote(note string) string {
	note = strings.TrimSpace(note)
	if note == "" {
		return ""
	}
	note = strings.ReplaceAll(note, model.PaidAtDefaultedNote, "未识别到支付时间")
	if !hasHan(note) {
		return ""
	}
	return clipPhrase(chinesePhrase(note))
}

func reasonKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimRight(s, ".。!！?？,，")
	return strings.TrimSpace(s)
}

func matchEnglishReason(notes string) (string, bool) {
	s := strings.ToLower(strings.Join(strings.Fields(notes), " "))
	for _, rule := range englishReasonRules {
		for _, needle := range rule.needles {
			if strings.Contains(s, needle) {
				return rule.phrase, true
			}
		}
	}
	return "", false
}

// chinesePhrase keeps a short zh-Hans clause and drops Latin words.
func chinesePhrase(notes string) string {
	s := redactForPhone(notes)
	s = notifyPhrase(s)
	s = asciiLetters.ReplaceAllString(s, " ")
	s = strings.Trim(s, " \t[]()（）【】/、;；:：-—~\"'`")
	s = strings.Join(strings.Fields(s), " ")
	if !hasHan(s) {
		return ""
	}
	return s
}

func clipPhrase(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) <= reasonRunes {
		return s
	}
	return string([]rune(s)[:reasonRunes]) + "…"
}

// notifyPhrase keeps the text before the first comma and drops full stops.
func notifyPhrase(s string) string {
	s = strings.TrimSpace(s)
	cut := len(s)
	for _, sep := range []string{"，", ","} {
		if i := strings.Index(s, sep); i >= 0 && i < cut {
			cut = i
		}
	}
	if cut < len(s) {
		s = s[:cut]
	}
	s = strings.NewReplacer(
		"。", "",
		".", "",
		"！", "",
		"!", "",
		"？", "",
		"?", "",
	).Replace(s)
	return strings.TrimSpace(s)
}

func redactForPhone(s string) string {
	s = bearerRE.ReplaceAllString(s, " ")
	s = keyRE.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(s), " ")
}

func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
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
