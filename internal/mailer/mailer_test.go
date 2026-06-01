package mailer

import (
	"bytes"
	"context"
	"html/template"
	"strings"
	"testing"
)

func TestRenderOTP_ContainsInjectedValues(t *testing.T) {
	m := New(Config{})

	data := otpData{
		Name:        "Alice",
		Heading:     "Confirm your email address",
		Intro:       "Enter the code below to verify your email. It expires in 10 minutes.",
		Code:        "123456",
		Note:        "If you didn't create an account, you can safely ignore this email.",
		PrivacyText: "Privacy Policy",
		PrivacyURL:  "https://tabslate.com/en/privacy-policy",
		TermsText:   "Terms",
		TermsURL:    "https://tabslate.com/en/terms",
	}

	var buf bytes.Buffer
	if err := m.tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("execute template: %v", err)
	}

	html := buf.String()
	for _, want := range []string{
		data.Heading,
		"Hi Alice,",
		data.Intro,
		data.Code,
		data.PrivacyText,
		data.PrivacyURL,
		data.TermsText,
		data.TermsURL,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("rendered html missing %q", want)
		}
	}
	if !strings.Contains(html, template.HTMLEscapeString(data.Note)) {
		t.Fatalf("rendered html missing escaped note %q", data.Note)
	}

	headingIdx := strings.Index(html, data.Heading)
	greetingIdx := strings.Index(html, "Hi Alice,")
	if headingIdx == -1 || greetingIdx == -1 {
		t.Fatalf("missing heading or greeting in rendered html")
	}
	if greetingIdx < headingIdx {
		t.Fatalf("greeting appeared above heading")
	}
}

func TestSendOTP_AllPurposeLangCombinations(t *testing.T) {
	m := New(Config{})

	cases := []struct {
		purpose string
		lang    string
	}{
		{purpose: "verify", lang: "en"},
		{purpose: "verify", lang: "zh"},
		{purpose: "reset", lang: "en"},
		{purpose: "reset", lang: "zh"},
	}

	for _, tc := range cases {
		if err := m.SendOTP(context.Background(), "test@example.com", "Bob", "654321", tc.purpose, tc.lang); err != nil {
			t.Fatalf("SendOTP(%s, %s) returned error: %v", tc.purpose, tc.lang, err)
		}
	}
}

func TestOTPTranslationsAndLegalLinks(t *testing.T) {
	tests := []struct {
		purpose     string
		lang        string
		subject     string
		privacyText string
		privacyURL  string
		termsText   string
		termsURL    string
	}{
		{
			purpose:     "verify",
			lang:        "en",
			subject:     "Verify your TabSlate email",
			privacyText: "Privacy Policy",
			privacyURL:  "https://tabslate.com/en/privacy-policy",
			termsText:   "Terms",
			termsURL:    "https://tabslate.com/en/terms",
		},
		{
			purpose:     "verify",
			lang:        "zh",
			subject:     "验证您的 TabSlate 邮箱",
			privacyText: "隐私政策",
			privacyURL:  "https://tabslate.com/zh/privacy-policy",
			termsText:   "服务条款",
			termsURL:    "https://tabslate.com/zh/terms",
		},
		{
			purpose:     "reset",
			lang:        "en",
			subject:     "Reset your TabSlate password",
			privacyText: "Privacy Policy",
			privacyURL:  "https://tabslate.com/en/privacy-policy",
			termsText:   "Terms",
			termsURL:    "https://tabslate.com/en/terms",
		},
		{
			purpose:     "reset",
			lang:        "zh",
			subject:     "重置您的 TabSlate 密码",
			privacyText: "隐私政策",
			privacyURL:  "https://tabslate.com/zh/privacy-policy",
			termsText:   "服务条款",
			termsURL:    "https://tabslate.com/zh/terms",
		},
	}

	for _, tc := range tests {
		stringsByLang, ok := translations[tc.purpose]
		if !ok {
			t.Fatalf("missing purpose %q", tc.purpose)
		}
		copy, ok := stringsByLang[tc.lang]
		if !ok {
			t.Fatalf("missing language %q for purpose %q", tc.lang, tc.purpose)
		}
		if copy.Subject != tc.subject {
			t.Fatalf("subject mismatch for %s/%s: got %q want %q", tc.purpose, tc.lang, copy.Subject, tc.subject)
		}

		links, ok := legalLinks[tc.lang]
		if !ok {
			t.Fatalf("missing legal links for %q", tc.lang)
		}
		if links.PrivacyText != tc.privacyText || links.PrivacyURL != tc.privacyURL {
			t.Fatalf("privacy link mismatch for %s", tc.lang)
		}
		if links.TermsText != tc.termsText || links.TermsURL != tc.termsURL {
			t.Fatalf("terms link mismatch for %s", tc.lang)
		}
	}
}
