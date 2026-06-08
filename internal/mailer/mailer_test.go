package mailer

import (
	"bytes"
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

func TestRenderOTPLocalizedContent(t *testing.T) {
	m := New(Config{})

	cases := []struct {
		name        string
		purpose     string
		lang        string
		subject     string
		heading     string
		intro       string
		note        string
		greeting    string
		privacyText string
		privacyURL  string
		termsText   string
		termsURL    string
	}{
		{
			name:        "verify-en",
			purpose:     "verify",
			lang:        "en",
			subject:     "Verify your TabSlate email",
			heading:     "Confirm your email address",
			intro:       "Enter the code below to verify your email. It expires in 10 minutes.",
			note:        "If you didn't create an account, you can safely ignore this email.",
			greeting:    "Hi Bob,",
			privacyText: "Privacy Policy",
			privacyURL:  "https://tabslate.com/en/privacy-policy",
			termsText:   "Terms",
			termsURL:    "https://tabslate.com/en/terms",
		},
		{
			name:        "verify-zh",
			purpose:     "verify",
			lang:        "zh",
			subject:     "验证您的 TabSlate 邮箱",
			heading:     "确认您的邮箱地址",
			intro:       "请在下方输入验证码完成邮箱验证，验证码 10 分钟内有效。",
			note:        "如果您没有注册账号，请忽略此邮件。",
			greeting:    "Hi Bob,",
			privacyText: "隐私政策",
			privacyURL:  "https://tabslate.com/zh/privacy-policy",
			termsText:   "服务条款",
			termsURL:    "https://tabslate.com/zh/terms",
		},
		{
			name:        "reset-en",
			purpose:     "reset",
			lang:        "en",
			subject:     "Reset your TabSlate password",
			heading:     "Reset your password",
			intro:       "Enter the code below to reset your password. It expires in 10 minutes.",
			note:        "If you didn't request a password reset, you can safely ignore this email.",
			greeting:    "Hi Bob,",
			privacyText: "Privacy Policy",
			privacyURL:  "https://tabslate.com/en/privacy-policy",
			termsText:   "Terms",
			termsURL:    "https://tabslate.com/en/terms",
		},
		{
			name:        "reset-zh",
			purpose:     "reset",
			lang:        "zh",
			subject:     "重置您的 TabSlate 密码",
			heading:     "重置密码",
			intro:       "请在下方输入验证码以重置密码，验证码 10 分钟内有效。",
			note:        "如果您没有申请重置密码，请忽略此邮件。",
			greeting:    "Hi Bob,",
			privacyText: "隐私政策",
			privacyURL:  "https://tabslate.com/zh/privacy-policy",
			termsText:   "服务条款",
			termsURL:    "https://tabslate.com/zh/terms",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			subject, body, err := m.renderOTP("Bob", "654321", tc.purpose, tc.lang)
			if err != nil {
				t.Fatalf("renderOTP(%s, %s) returned error: %v", tc.purpose, tc.lang, err)
			}
			if subject != tc.subject {
				t.Fatalf("subject mismatch: got %q want %q", subject, tc.subject)
			}
			for _, want := range []string{
				tc.heading,
				tc.greeting,
				tc.intro,
				"654321",
				template.HTMLEscapeString(tc.note),
				tc.privacyText,
				tc.privacyURL,
				tc.termsText,
				tc.termsURL,
			} {
				if !strings.Contains(body, want) {
					t.Fatalf("rendered body missing %q", want)
				}
			}
		})
	}
}

func TestRenderOTPFallsBackToEnglishForUnknownLang(t *testing.T) {
	m := New(Config{})

	subject, body, err := m.renderOTP("Bob", "654321", "verify", "fr")
	if err != nil {
		t.Fatalf("renderOTP returned error: %v", err)
	}
	if subject != "Verify your TabSlate email" {
		t.Fatalf("subject mismatch: got %q", subject)
	}
	for _, want := range []string{
		"Confirm your email address",
		"Enter the code below to verify your email. It expires in 10 minutes.",
		"Privacy Policy",
		"https://tabslate.com/en/privacy-policy",
		"Terms",
		"https://tabslate.com/en/terms",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered body missing %q", want)
		}
	}
}

func TestRenderOTPRejectsUnknownPurpose(t *testing.T) {
	m := New(Config{})

	if _, _, err := m.renderOTP("Bob", "654321", "magic", "en"); err == nil {
		t.Fatal("renderOTP returned nil error for unknown purpose")
	}
}
