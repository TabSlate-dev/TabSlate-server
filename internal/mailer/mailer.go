// Package mailer provides email delivery via SMTP, Resend, or Amazon SES.
package mailer

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/smtp"
	"time"
)

//go:embed templates/*.html
var tmplFS embed.FS

type otpData struct {
	Name        string
	Heading     string
	Intro       string
	Code        string
	Note        string
	PrivacyText string
	PrivacyURL  string
	TermsText   string
	TermsURL    string
}

type otpStrings struct {
	Subject string
	Heading string
	Intro   string
	Note    string
}

type legalLinkSet struct {
	PrivacyText string
	PrivacyURL  string
	TermsText   string
	TermsURL    string
}

var translations = map[string]map[string]otpStrings{
	"verify": {
		"en": {
			Subject: "Verify your TabSlate email",
			Heading: "Confirm your email address",
			Intro:   "Enter the code below to verify your email. It expires in 10 minutes.",
			Note:    "If you didn't create an account, you can safely ignore this email.",
		},
		"zh": {
			Subject: "验证您的 TabSlate 邮箱",
			Heading: "确认您的邮箱地址",
			Intro:   "请在下方输入验证码完成邮箱验证，验证码 10 分钟内有效。",
			Note:    "如果您没有注册账号，请忽略此邮件。",
		},
	},
	"reset": {
		"en": {
			Subject: "Reset your TabSlate password",
			Heading: "Reset your password",
			Intro:   "Enter the code below to reset your password. It expires in 10 minutes.",
			Note:    "If you didn't request a password reset, you can safely ignore this email.",
		},
		"zh": {
			Subject: "重置您的 TabSlate 密码",
			Heading: "重置密码",
			Intro:   "请在下方输入验证码以重置密码，验证码 10 分钟内有效。",
			Note:    "如果您没有申请重置密码，请忽略此邮件。",
		},
	},
}

var legalLinks = map[string]legalLinkSet{
	"en": {
		PrivacyText: "Privacy Policy",
		PrivacyURL:  "https://tabslate.com/en/privacy-policy",
		TermsText:   "Terms",
		TermsURL:    "https://tabslate.com/en/terms",
	},
	"zh": {
		PrivacyText: "隐私政策",
		PrivacyURL:  "https://tabslate.com/zh/privacy-policy",
		TermsText:   "服务条款",
		TermsURL:    "https://tabslate.com/zh/terms",
	},
}

// AccountDeletionEmailData carries the computed execution date for deletion emails.
// ExecutesAt is zero for the "deletion_executed" purpose (already happened).
type AccountDeletionEmailData struct {
	ExecutesAt time.Time
}

type deletionStrings struct {
	Subject string
	Heading string
	// Intro may contain a single %s placeholder for the formatted execution date.
	// If ExecutesAt is zero, Intro is used as-is.
	Intro string
	Note  string
}

type deletionEmailData struct {
	Name        string
	Heading     string
	Intro       string
	Note        string
	PrivacyText string
	PrivacyURL  string
	TermsText   string
	TermsURL    string
}

var deletionTranslations = map[string]map[string]deletionStrings{
	"deletion_requested": {
		"en": {
			Subject: "Your TabSlate account deletion is scheduled",
			Heading: "Account deletion scheduled",
			Intro:   "We've received your account deletion request. Your account and all associated data will be permanently deleted on %s.\n\nTo cancel this request, simply log in to your TabSlate account before that date — no other action is required.",
			Note:    "If you didn't request this, please log in immediately to cancel the deletion.",
		},
		"zh": {
			Subject: "您的 TabSlate 账号注销申请已受理",
			Heading: "账号注销申请已受理",
			Intro:   "我们已收到您的账号注销申请。您的账号及所有关联数据将于 %s 被永久删除。\n\n如需取消注销，只需在此日期之前重新登录 TabSlate 即可——无需其他任何操作。",
			Note:    "如果您未发起此申请，请立即登录以取消注销。",
		},
	},
	"deletion_reminder": {
		"en": {
			Subject: "Your TabSlate account will be deleted in 3 days",
			Heading: "Account deletion in 3 days",
			Intro:   "This is a reminder that your TabSlate account is scheduled for permanent deletion on %s.\n\nTo cancel, simply log in to your account before that date — no other action is required.",
			Note:    "If you want to keep your account, log in before the deadline.",
		},
		"zh": {
			Subject: "您的 TabSlate 账号将在 3 天后注销",
			Heading: "账号将在 3 天后注销",
			Intro:   "提醒您：您的 TabSlate 账号已计划于 %s 永久删除。\n\n如需取消，只需在此日期之前重新登录账号即可——无需其他操作。",
			Note:    "如果您希望保留账号，请在截止日期前登录。",
		},
	},
	"deletion_executed": {
		"en": {
			Subject: "Your TabSlate account has been deleted",
			Heading: "Account deleted",
			Intro:   "Your TabSlate account and all associated data have been permanently deleted. Thank you for using TabSlate.",
			Note:    "If you didn't request account deletion, please contact us at privacy@cs.tabslate.com.",
		},
		"zh": {
			Subject: "您的 TabSlate 账号已注销",
			Heading: "账号已注销",
			Intro:   "您的 TabSlate 账号及所有关联数据已永久删除。感谢您使用 TabSlate。",
			Note:    "如果您未申请注销账号，请通过 privacy@cs.tabslate.com 联系我们。",
		},
	},
}

// Mailer sends transactional emails.
// If provider is empty, sending is a no-op (useful for dev / self-hosted without email).
type Mailer struct {
	provider string // "smtp", "resend", "ses", or "" (disabled)

	// SMTP
	smtpHost string
	smtpPort string
	smtpUser string
	smtpPass string
	smtpFrom string

	// Resend
	resendAPIKey string
	resendFrom   string

	// SES
	sesAccessKeyID string
	sesSecretKey   string
	sesRegion      string
	sesFrom        string

	client       *http.Client
	tmpl         *template.Template
	deletionTmpl *template.Template
}

// Config holds the mail provider configuration.
type Config struct {
	Provider string

	// SMTP
	SMTPHost     string
	SMTPPort     string
	SMTPUser     string
	SMTPPassword string
	SMTPFrom     string

	// Resend
	ResendAPIKey string
	ResendFrom   string

	// SES
	SESAccessKeyID string
	SESSecretKey   string
	SESRegion      string
	SESFrom        string
}

// New creates a Mailer from the given config.
// If Config.Provider is empty, the mailer is disabled (Send is a no-op).
func New(cfg Config) *Mailer {
	return &Mailer{
		provider:       cfg.Provider,
		smtpHost:       cfg.SMTPHost,
		smtpPort:       cfg.SMTPPort,
		smtpUser:       cfg.SMTPUser,
		smtpPass:       cfg.SMTPPassword,
		smtpFrom:       cfg.SMTPFrom,
		resendAPIKey:   cfg.ResendAPIKey,
		resendFrom:     cfg.ResendFrom,
		sesAccessKeyID: cfg.SESAccessKeyID,
		sesSecretKey:   cfg.SESSecretKey,
		sesRegion:      cfg.SESRegion,
		sesFrom:        cfg.SESFrom,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
		tmpl:         template.Must(template.ParseFS(tmplFS, "templates/otp.html")),
		deletionTmpl: template.Must(template.ParseFS(tmplFS, "templates/account_deletion.html")),
	}
}

// Enabled reports whether the mailer has a configured provider.
func (m *Mailer) Enabled() bool {
	return m.provider != ""
}

// Send delivers an email. It is a no-op if no provider is configured.
func (m *Mailer) Send(ctx context.Context, to, subject, htmlBody string) error {
	switch m.provider {
	case "smtp":
		return m.sendSMTP(to, subject, htmlBody)
	case "resend":
		return m.sendResend(ctx, to, subject, htmlBody)
	case "ses":
		return m.sendSES(ctx, to, subject, htmlBody)
	default:
		// Disabled — silently succeed so that dev environments work without email.
		return nil
	}
}

// SendOTP renders the OTP email template and sends it.
func (m *Mailer) SendOTP(ctx context.Context, to, name, code, purpose, lang string) error {
	subject, body, err := m.renderOTP(name, code, purpose, lang)
	if err != nil {
		return err
	}

	return m.Send(ctx, to, subject, body)
}

// SendAccountDeletion renders the account-deletion email and sends it.
// purpose: "deletion_requested" | "deletion_reminder" | "deletion_executed"
func (m *Mailer) SendAccountDeletion(ctx context.Context, to, name, purpose, lang string, data AccountDeletionEmailData) error {
	subject, body, err := m.renderAccountDeletion(name, purpose, lang, data)
	if err != nil {
		return err
	}
	return m.Send(ctx, to, subject, body)
}

func (m *Mailer) renderOTP(name, code, purpose, lang string) (string, string, error) {
	byPurpose, ok := translations[purpose]
	if !ok {
		return "", "", fmt.Errorf("unknown otp purpose %q", purpose)
	}

	copy := byPurpose["en"]
	if byLang, ok := byPurpose[lang]; ok {
		copy = byLang
	}

	links := legalLinks["en"]
	if localized, ok := legalLinks[lang]; ok {
		links = localized
	}

	var body bytes.Buffer
	if err := m.tmpl.Execute(&body, otpData{
		Name:        name,
		Heading:     copy.Heading,
		Intro:       copy.Intro,
		Code:        code,
		Note:        copy.Note,
		PrivacyText: links.PrivacyText,
		PrivacyURL:  links.PrivacyURL,
		TermsText:   links.TermsText,
		TermsURL:    links.TermsURL,
	}); err != nil {
		return "", "", fmt.Errorf("render otp template: %w", err)
	}

	return copy.Subject, body.String(), nil
}

func (m *Mailer) renderAccountDeletion(name, purpose, lang string, data AccountDeletionEmailData) (string, string, error) {
	byPurpose, ok := deletionTranslations[purpose]
	if !ok {
		return "", "", fmt.Errorf("unknown deletion purpose %q", purpose)
	}

	strs := byPurpose["en"]
	if byLang, ok := byPurpose[lang]; ok {
		strs = byLang
	}

	links := legalLinks["en"]
	if localized, ok := legalLinks[lang]; ok {
		links = localized
	}

	intro := strs.Intro
	if !data.ExecutesAt.IsZero() {
		intro = fmt.Sprintf(strs.Intro, data.ExecutesAt.Format("January 2, 2006"))
	}

	var body bytes.Buffer
	if err := m.deletionTmpl.Execute(&body, deletionEmailData{
		Name:        name,
		Heading:     strs.Heading,
		Intro:       intro,
		Note:        strs.Note,
		PrivacyText: links.PrivacyText,
		PrivacyURL:  links.PrivacyURL,
		TermsText:   links.TermsText,
		TermsURL:    links.TermsURL,
	}); err != nil {
		return "", "", fmt.Errorf("render account_deletion template: %w", err)
	}

	return strs.Subject, body.String(), nil
}

func (m *Mailer) sendSMTP(to, subject, htmlBody string) error {
	addr := m.smtpHost + ":" + m.smtpPort

	msg := "From: " + m.smtpFrom + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/html; charset=\"UTF-8\"\r\n" +
		"\r\n" +
		htmlBody

	var auth smtp.Auth
	if m.smtpUser != "" {
		auth = smtp.PlainAuth("", m.smtpUser, m.smtpPass, m.smtpHost)
	}

	if err := smtp.SendMail(addr, auth, m.smtpFrom, []string{to}, []byte(msg)); err != nil {
		return fmt.Errorf("smtp send: %w", err)
	}
	return nil
}

type resendRequest struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	HTML    string   `json:"html"`
}

func (m *Mailer) sendResend(ctx context.Context, to, subject, htmlBody string) error {
	body, err := json.Marshal(resendRequest{
		From:    m.resendFrom,
		To:      []string{to},
		Subject: subject,
		HTML:    htmlBody,
	})
	if err != nil {
		return fmt.Errorf("resend marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("resend request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.resendAPIKey)

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("resend send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("resend: unexpected status %d", resp.StatusCode)
	}
	return nil
}
