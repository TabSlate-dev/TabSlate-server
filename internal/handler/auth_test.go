package handler

import "testing"

func TestParseLang(t *testing.T) {
	tests := []struct {
		name       string
		acceptLang string
		want       string
	}{
		{name: "zh-CN", acceptLang: "zh-CN,zh;q=0.9,en;q=0.8", want: "zh"},
		{name: "zh", acceptLang: "zh", want: "zh"},
		{name: "zh-TW", acceptLang: "zh-TW,zh;q=0.9", want: "zh"},
		{name: "en-US", acceptLang: "en-US,en;q=0.9", want: "en"},
		{name: "en", acceptLang: "en", want: "en"},
		{name: "fr-FR", acceptLang: "fr-FR,fr;q=0.9", want: "en"},
		{name: "empty", acceptLang: "", want: "en"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseLang(tc.acceptLang); got != tc.want {
				t.Fatalf("parseLang(%q) = %q, want %q", tc.acceptLang, got, tc.want)
			}
		})
	}
}
