package api

import "testing"

func TestNotBillReasonAlwaysChinese(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "这张图不是付款账单"},
		{"not a bill", "这张图不是付款账单"},
		{"This is a screenshot, not a bill.", "这张图不是付款账单"},
		{"this is a screenshot not a bill", "这张图不是付款账单"},
		{"chat screenshot", "聊天截图"},
		{"WeChat conversation", "聊天截图"},
		{"Home screen screenshot", "主屏幕截图"},
		{"a landscape photo", "风景照片"},
		{"image is nearly a solid color, so it was not treated as a receipt", "图片几乎是纯色"},
		{"image is smaller than 64x64", "图片太小"},
		{"empty image", "图片是空的"},
		{"image is not a decodable png, jpeg, or webp", "图片无法解码"},
		{"some random english caption", "这张图不是付款账单"},
		{"聊天记录，不是付款账单。", "聊天记录"},
		{"聊天记录,后面不要", "聊天记录"},
		{"空白图片。", "空白图片"},
		{"这是 screenshot，不是账单", "这是"},
	}
	for _, tc := range cases {
		got := notBillReason(tc.in)
		if got != tc.want {
			t.Errorf("notBillReason(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if asciiLetters.MatchString(got) || stringsContainsStop(got) {
			t.Errorf("phrase not short zh-Hans: %q from %q", got, tc.in)
		}
	}
}

func TestPhoneNoteDropsEnglish(t *testing.T) {
	if got := phoneNote("mock extractor: deterministic fields from sha256"); got != "" {
		t.Fatalf("english note kept: %q", got)
	}
	if got := phoneNote("paid_at missing; defaulted to upload time"); got != "未识别到支付时间" {
		t.Fatalf("defaulted note: %q", got)
	}
	if got := phoneNote("午餐，公司附近。"); got != "午餐" {
		t.Fatalf("chinese note: %q", got)
	}
	if asciiLetters.MatchString(phoneNote("paid_at missing; defaulted to upload time")) {
		t.Fatal("defaulted note still has english")
	}
}

func stringsContainsStop(s string) bool {
	for _, r := range []rune{'，', ',', '。', '.', '！', '!', '？', '?'} {
		for _, c := range s {
			if c == r {
				return true
			}
		}
	}
	return false
}
