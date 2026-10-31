package i18n

import "testing"

func TestResolve(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"zh_CN.UTF-8", "zh-CN"}, {"zh-SG", "zh-CN"}, {"zh-Hans", "zh-CN"}, {"zh", "zh-CN"},
		{"zh-TW", "en"}, {"zh-Hant", "en"}, {"zh-Hant-CN", "en"}, {"ja-JP", "en"}, {"en-US", "en"}, {"C", "en"}, {"", "en"},
	} {
		if got := Resolve(tc.in); got != tc.want {
			t.Errorf("Resolve(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
