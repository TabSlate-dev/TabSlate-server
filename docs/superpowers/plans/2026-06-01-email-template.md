# Email Template Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace inline `fmt.Sprintf` HTML in `sendOTPEmail` with a branded `html/template`-backed system supporting English and Chinese; the Chrome extension sends `Accept-Language` so the correct language is used.

**Architecture:** The `mailer` package gains `internal/mailer/templates/otp.html` (embedded via `embed.FS`), a `SendOTP` high-level method that selects copy by `purpose×lang`, renders the template, and calls the existing `Send`. The handler's `sendOTPEmail` becomes a one-liner extracting `lang` from `Accept-Language`. The extension reads from i18n store and passes the resolved locale header on the 4 OTP endpoints.

**Tech Stack:** Go `html/template`, `embed.FS`, Gin `c.GetHeader`; TypeScript `zustand`, WXT

---

## File Map

| Action | File | What changes |
|--------|------|-------------|
| Create | `internal/mailer/templates/otp.html` | Branded HTML template with 5 Go template variables |
| Modify | `internal/mailer/mailer.go` | Add embed, `tmpl` field, `otpData`, `otpStrings`, `translations`, `SendOTP` |
| Create | `internal/mailer/mailer_test.go` | Tests for template rendering and `SendOTP` |
| Modify | `internal/handler/auth.go` | Add `parseLang`, update `sendOTPEmail`, update 4 call sites |
| Create | `internal/handler/auth_test.go` | Tests for `parseLang` |
| Delete | `template.html` (repo root) | Source template no longer needed |
| Modify | `TabSlate/store/i18n-store.ts` | Export `resolveAcceptLanguage` helper |
| Modify | `TabSlate/lib/api.ts` | Add `lang?: string` to 4 functions, set `Accept-Language` header |
| Modify | `TabSlate/store/auth-store.ts` | Read i18n store language, pass to api in 4 methods |

---

## Task 1: Create the HTML template

**Files:**
- Create: `internal/mailer/templates/otp.html`

- [ ] **Step 1: Create the template file**

Create `internal/mailer/templates/otp.html` with this exact content:

```html
<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Transitional//EN" "http://www.w3.org/TR/xhtml1/DTD/xhtml1-transitional.dtd">
<html dir="ltr" lang="en">
  <head>
    <meta content="text/html; charset=UTF-8" http-equiv="Content-Type" />
    <meta name="x-apple-disable-message-reformatting" />
    <meta name="color-scheme" content="light dark" />
    <meta name="supported-color-schemes" content="light dark" />
    <style>
      @media(prefers-color-scheme:dark){.dark_bg{background-color:rgb(10,10,10) !important}.dark_text{color:rgb(250,250,250) !important}.dark_bg2{background-color:rgb(43,43,43) !important}.dark_muted{color:rgb(212,212,212) !important}}
    </style>
  </head>
  <body class="dark_bg dark_text" style="background-color:rgb(255,255,255);color:rgb(10,10,10)">
    <!--$-->
    <table border="0" width="100%" cellpadding="0" cellspacing="0" role="presentation" align="center">
      <tbody>
        <tr>
          <td style="background-color:rgb(255,255,255);color:rgb(10,10,10)">
            <table align="center" width="100%" border="0" cellpadding="0" cellspacing="0" role="presentation"
              style="max-width:600px;padding-left:0;padding-right:0">
              <tbody>
                <tr style="width:100%">
                  <td>
                    <!-- Logo -->
                    <table border="0" cellpadding="0" cellspacing="0" role="presentation">
                      <tr>
                        <td>
                          <img alt="TabSlate" src="https://tabslate.com/logo.svg"
                            style="height:32px;width:auto;display:block;outline:none;border:none;text-decoration:none" />
                        </td>
                        <td style="padding-left:8px;font-size:20px;font-weight:700;vertical-align:middle;">TabSlate</td>
                      </tr>
                    </table>
                    <!-- Greeting + Heading -->
                    <div style="padding-top:16px;padding-bottom:2px;padding-left:4px;padding-right:4px;text-align:left">
                      <p style="font-size:16px;line-height:1.75;margin:0 0 8px 0;">Hi {{.Name}},</p>
                      <h1 style="font-size:36px;line-height:36px;font-weight:800;letter-spacing:-0.4px;margin:0;padding:0">
                        <span style="font-weight:700">{{.Heading}}</span>
                      </h1>
                    </div>
                    <!-- Intro text -->
                    <div style="padding-top:3px;padding-bottom:3px;text-align:left">
                      <p style="font-size:18px;line-height:28px;margin:0;padding:0">
                        <span>{{.Intro}}</span>
                      </p>
                    </div>
                    <!-- Spacer -->
                    <div style="padding-top:3px;padding-bottom:3px;text-align:left">
                      <p style="font-size:16px;line-height:1.75;margin:0;padding:0;">&nbsp;</p>
                    </div>
                    <!-- OTP Code -->
                    <div class="dark_bg2" style="padding:8px 4px;background-color:rgb(240,240,240);text-align:center">
                      <h1 style="font-size:36px;line-height:36px;font-weight:800;letter-spacing:0.4em;margin:0;padding:8px 0;">
                        {{.Code}}
                      </h1>
                    </div>
                    <!-- Spacer -->
                    <div style="padding-top:3px;padding-bottom:3px;text-align:left">
                      <p style="font-size:16px;line-height:1.75;margin:0;padding:0;">&nbsp;</p>
                    </div>
                    <!-- Footer note -->
                    <div style="padding-top:3px;padding-bottom:3px;text-align:left">
                      <p style="font-size:14px;line-height:20px;margin:0;padding:0;">
                        <span>{{.Note}}</span>
                      </p>
                    </div>
                    <hr style="width:100%;border:none;border-top:1px solid #eaeaea" />
                    <!-- Footer logo -->
                    <table border="0" cellpadding="0" cellspacing="0" role="presentation">
                      <tr>
                        <td>
                          <img alt="TabSlate" src="https://tabslate.com/logo.svg"
                            style="height:24px;width:auto;display:block;outline:none;border:none;text-decoration:none" />
                        </td>
                        <td style="padding-left:6px;font-size:14px;font-weight:700;vertical-align:middle;">TabSlate</td>
                      </tr>
                    </table>
                    <!-- Copyright -->
                    <div style="padding-top:8px;padding-bottom:3px;text-align:left">
                      <p style="font-size:14px;line-height:20px;margin:0;padding:0;">
                        <span class="dark_muted" style="color:rgb(115,115,115);">©2026 TabSlate. All rights reserved.</span>
                      </p>
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>
          </td>
        </tr>
      </tbody>
    </table>
    <!--7--><!--/$-->
  </body>
</html>
```

- [ ] **Step 2: Commit**

```bash
git add internal/mailer/templates/otp.html
git commit -m "feat: add branded OTP email HTML template"
```

---

## Task 2: Add embed + template infrastructure to mailer.go

**Files:**
- Modify: `internal/mailer/mailer.go`
- Create: `internal/mailer/mailer_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/mailer/mailer_test.go`:

```go
package mailer

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderOTP_ContainsInjectedValues(t *testing.T) {
	m := New(Config{})
	var buf bytes.Buffer
	err := m.tmpl.Execute(&buf, otpData{
		Name:    "Alice",
		Heading: "Test Heading",
		Intro:   "Test intro text.",
		Code:    "123456",
		Note:    "Test note.",
	})
	if err != nil {
		t.Fatalf("template.Execute: %v", err)
	}
	html := buf.String()
	for _, want := range []string{"Alice", "Test Heading", "Test intro text.", "123456", "Test note."} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered HTML missing %q", want)
		}
	}
}
```

- [ ] **Step 2: Run test — expect compile failure (types not defined yet)**

```bash
cd /path/to/TabSlate-server
go test ./internal/mailer/... -run TestRenderOTP_ContainsInjectedValues -v
```

Expected: compile error — `m.tmpl` undefined, `otpData` undefined

- [ ] **Step 3: Add embed + tmpl to mailer.go**

At the top of `internal/mailer/mailer.go`, update the import block and add the embed directive + new type. The full updated top section:

```go
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
	Name, Heading, Intro, Code, Note string
}
```

Add `tmpl *template.Template` field to the `Mailer` struct (after the existing `client` field):

```go
type Mailer struct {
	provider string

	smtpHost string
	smtpPort string
	smtpUser string
	smtpPass string
	smtpFrom string

	resendAPIKey string
	resendFrom   string

	sesAccessKeyID string
	sesSecretKey   string
	sesRegion      string
	sesFrom        string

	client *http.Client
	tmpl   *template.Template
}
```

Update `New()` to parse the template. Add these two lines before `return m`:

```go
func New(cfg Config) *Mailer {
	m := &Mailer{
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
	}
	m.tmpl = template.Must(template.ParseFS(tmplFS, "templates/otp.html"))
	return m
}
```

- [ ] **Step 4: Run test — expect PASS**

```bash
go test ./internal/mailer/... -run TestRenderOTP_ContainsInjectedValues -v
```

Expected:
```
--- PASS: TestRenderOTP_ContainsInjectedValues (0.00s)
PASS
```

- [ ] **Step 5: Commit**

```bash
git add internal/mailer/mailer.go internal/mailer/mailer_test.go
git commit -m "feat: embed OTP template into mailer binary"
```

---

## Task 3: Add translations map and SendOTP method

**Files:**
- Modify: `internal/mailer/mailer.go`
- Modify: `internal/mailer/mailer_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/mailer/mailer_test.go`:

```go
import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestSendOTP_DisabledMailer_ReturnsNil(t *testing.T) {
	m := New(Config{}) // provider="" → Send is a no-op
	err := m.SendOTP(context.Background(), "test@example.com", "Alice", "123456", "verify", "en")
	if err != nil {
		t.Fatalf("SendOTP with disabled mailer returned error: %v", err)
	}
}

func TestSendOTP_AllPurposeLangCombinations(t *testing.T) {
	m := New(Config{})
	cases := []struct{ purpose, lang string }{
		{"verify", "en"},
		{"verify", "zh"},
		{"reset", "en"},
		{"reset", "zh"},
		{"verify", "fr"},    // unknown lang → fallback to "en"
		{"unknown", "en"},   // unknown purpose → fallback to verify
	}
	for _, c := range cases {
		if err := m.SendOTP(context.Background(), "x@x.com", "Bob", "654321", c.purpose, c.lang); err != nil {
			t.Errorf("purpose=%q lang=%q: %v", c.purpose, c.lang, err)
		}
	}
}

func TestSendOTP_SubjectContainsTabSlate(t *testing.T) {
	// Use a capture mailer by temporarily wrapping — instead, test via template render.
	// Verify that translations include "TabSlate" in each subject.
	for purpose, langs := range translations {
		for lang, s := range langs {
			if !strings.Contains(s.Subject, "TabSlate") {
				t.Errorf("translations[%q][%q].Subject missing 'TabSlate': %q", purpose, lang, s.Subject)
			}
		}
	}
}
```

- [ ] **Step 2: Run tests — expect compile failure (SendOTP not defined)**

```bash
go test ./internal/mailer/... -v
```

Expected: compile error — `m.SendOTP` undefined, `translations` undefined

- [ ] **Step 3: Add otpStrings, translations, and SendOTP to mailer.go**

Add after the `otpData` type definition in `mailer.go`:

```go
type otpStrings struct {
	Subject, Heading, Intro, Note string
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

// SendOTP renders the OTP email template and sends it.
// purpose: "verify" or "reset". lang: "en" or "zh" (unknown values fall back to "en").
func (m *Mailer) SendOTP(ctx context.Context, to, name, code, purpose, lang string) error {
	purposeMap, ok := translations[purpose]
	if !ok {
		purposeMap = translations["verify"]
	}
	s, ok := purposeMap[lang]
	if !ok {
		s = purposeMap["en"]
	}
	var buf bytes.Buffer
	if err := m.tmpl.Execute(&buf, otpData{
		Name:    name,
		Heading: s.Heading,
		Intro:   s.Intro,
		Code:    code,
		Note:    s.Note,
	}); err != nil {
		return fmt.Errorf("render otp email: %w", err)
	}
	return m.Send(ctx, to, s.Subject, buf.String())
}
```

- [ ] **Step 4: Run tests — expect all PASS**

```bash
go test ./internal/mailer/... -v
```

Expected:
```
--- PASS: TestRenderOTP_ContainsInjectedValues (0.00s)
--- PASS: TestSendOTP_DisabledMailer_ReturnsNil (0.00s)
--- PASS: TestSendOTP_AllPurposeLangCombinations (0.00s)
--- PASS: TestSendOTP_SubjectContainsTabSlate (0.00s)
PASS
```

- [ ] **Step 5: Commit**

```bash
git add internal/mailer/mailer.go internal/mailer/mailer_test.go
git commit -m "feat: add SendOTP with en/zh translations"
```

---

## Task 4: Update auth.go — parseLang + sendOTPEmail + 4 call sites

**Files:**
- Modify: `internal/handler/auth.go`
- Create: `internal/handler/auth_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/handler/auth_test.go`:

```go
package handler

import (
	"testing"
)

func TestParseLang(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"zh-CN,zh;q=0.9,en;q=0.8", "zh"},
		{"zh", "zh"},
		{"zh-TW", "zh"},
		{"en-US,en;q=0.9", "en"},
		{"en", "en"},
		{"fr-FR,fr;q=0.9", "en"},
		{"", "en"},
	}
	for _, c := range cases {
		if got := parseLang(c.input); got != c.want {
			t.Errorf("parseLang(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test — expect compile failure (parseLang not defined)**

```bash
go test ./internal/handler/... -run TestParseLang -v
```

Expected: compile error — `parseLang` undefined

- [ ] **Step 3: Add "strings" to auth.go imports and add parseLang**

In `internal/handler/auth.go`, add `"strings"` to the import block:

```go
import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tabslate/server/billing"
	"github.com/tabslate/server/db"
	"github.com/tabslate/server/internal/auth"
	"github.com/tabslate/server/internal/captcha"
	"github.com/tabslate/server/internal/mailer"
	"github.com/tabslate/server/internal/middleware"
	"github.com/tabslate/server/internal/model"
	"github.com/tabslate/server/internal/ratelimit"
	"github.com/tabslate/server/internal/store"
)
```

Add `parseLang` near the bottom of the file (before or after `sendOTPEmail`):

```go
// parseLang maps an Accept-Language header value to a supported language code.
// Returns "zh" if the header contains "zh", otherwise "en".
func parseLang(acceptLang string) string {
	if strings.Contains(acceptLang, "zh") {
		return "zh"
	}
	return "en"
}
```

- [ ] **Step 4: Run test — expect PASS**

```bash
go test ./internal/handler/... -run TestParseLang -v
```

Expected:
```
--- PASS: TestParseLang (0.00s)
PASS
```

- [ ] **Step 5: Replace sendOTPEmail body and update its signature**

Replace the existing `sendOTPEmail` function (lines ~721–745 in auth.go):

**Before:**
```go
// sendOTPEmail sends a 6-digit OTP email. purpose: "verify" or "reset".
func (h *AuthHandler) sendOTPEmail(to, name, code, purpose string) {
	var subject, intro, note string
	switch purpose {
	case "reset":
		subject = "Reset your TabSlate password"
		intro = "Use the code below to reset your password. It expires in 10 minutes."
		note = "If you didn't request a password reset, you can safely ignore this email."
	default:
		subject = "Verify your TabSlate email"
		intro = "Use the code below to verify your email address. It expires in 10 minutes."
		note = "If you didn't create an account, you can safely ignore this email."
	}

	body := fmt.Sprintf(`<html><body>
<p>Hi %s,</p>
<p>%s</p>
<p style="font-size:2em;letter-spacing:0.15em;font-weight:bold;">%s</p>
<p>%s</p>
</body></html>`, name, intro, code, note)

	if err := h.mailer.Send(context.Background(), to, subject, body); err != nil {
		log.Printf("failed to send OTP email to %s: %v", to, err)
	}
}
```

**After:**
```go
// sendOTPEmail sends a 6-digit OTP email. purpose: "verify" or "reset". lang: "en" or "zh".
func (h *AuthHandler) sendOTPEmail(to, name, code, purpose, lang string) {
	if err := h.mailer.SendOTP(context.Background(), to, name, code, purpose, lang); err != nil {
		log.Printf("failed to send OTP email to %s: %v", to, err)
	}
}
```

- [ ] **Step 6: Update the 4 call sites**

Each call site follows this pattern: extract `lang` before the goroutine, then pass it.

**auth.go line ~178 (Register handler):**

Before:
```go
go h.sendOTPEmail(req.Email, req.Name, otp, "verify")
```
After:
```go
lang := parseLang(c.GetHeader("Accept-Language"))
go h.sendOTPEmail(req.Email, req.Name, otp, "verify", lang)
```

**auth.go line ~261 (Login handler):**

Before:
```go
go h.sendOTPEmail(user.Email, user.Name, otp, "verify")
```
After:
```go
lang := parseLang(c.GetHeader("Accept-Language"))
go h.sendOTPEmail(user.Email, user.Name, otp, "verify", lang)
```

**auth.go line ~399 (ResendVerification handler):**

Before:
```go
go h.sendOTPEmail(req.Email, name, otp, "verify")
```
After:
```go
lang := parseLang(c.GetHeader("Accept-Language"))
go h.sendOTPEmail(req.Email, name, otp, "verify", lang)
```

**auth.go line ~458 (ForgotPassword handler):**

Before:
```go
go h.sendOTPEmail(req.Email, name, otp, "reset")
```
After:
```go
lang := parseLang(c.GetHeader("Accept-Language"))
go h.sendOTPEmail(req.Email, name, otp, "reset", lang)
```

Note: `lang` is extracted before `go` so we read from `c` synchronously on the request goroutine — safe for Gin's context pool.

- [ ] **Step 7: Remove unused fmt import from auth.go if it's now unused**

Check if `fmt` is still used elsewhere in `auth.go`:

```bash
grep -n '"fmt"' internal/handler/auth.go
grep -n 'fmt\.' internal/handler/auth.go | head -5
```

If `fmt.` appears in other places (it almost certainly does — `fmt.Errorf`, etc.), no change needed.

- [ ] **Step 8: Build and test**

```bash
go build ./...
go test ./internal/handler/... -v
```

Expected: clean build, `TestParseLang` passes

- [ ] **Step 9: Commit**

```bash
git add internal/handler/auth.go internal/handler/auth_test.go
git commit -m "feat: wire SendOTP into auth handler with Accept-Language support"
```

---

## Task 5: Cleanup and final verification

**Files:**
- Delete: `template.html` (repo root)

- [ ] **Step 1: Delete the source template from repo root**

```bash
git rm template.html
```

- [ ] **Step 2: Run full test suite and vet**

```bash
go test ./...
go vet ./...
```

Expected: all tests pass, no vet warnings.

- [ ] **Step 3: Commit**

```bash
git commit -m "chore: remove template.html now that otp.html is embedded in binary"
```

---

## Task 6: Frontend — send Accept-Language on OTP endpoints

**Repo:** TabSlate (Chrome extension)  
**Files:**
- Modify: `store/i18n-store.ts`
- Modify: `lib/api.ts`
- Modify: `store/auth-store.ts`

- [ ] **Step 1: Export resolveAcceptLanguage from i18n-store.ts**

Add after the `useI18nStore` definition in `store/i18n-store.ts`:

```ts
export function resolveAcceptLanguage(lang: SupportedLanguage): string {
  if (lang === "zh_CN") return "zh-CN";
  if (lang === "en") return "en";
  // "auto" → use the browser's locale (e.g. "zh-CN", "en-US")
  return navigator.language;
}
```

- [ ] **Step 2: Add lang parameter to 4 API functions in lib/api.ts**

Update `register`, `login`, `resendVerification`, and `forgotPassword` to accept an optional `lang?: string` and forward it as `Accept-Language`:

```ts
register(
  baseUrl: string,
  name: string,
  email: string,
  password: string,
  captchaToken?: string,
  lang?: string,
): Promise<AuthResponse> {
  return request<AuthResponse>(baseUrl, "/auth/register", {
    method: "POST",
    body: JSON.stringify({ name, email, password, captcha_token: captchaToken }),
    headers: lang ? { "Accept-Language": lang } : undefined,
  });
},

login(
  baseUrl: string,
  email: string,
  password: string,
  captchaToken?: string,
  lang?: string,
): Promise<AuthResponse> {
  return request<AuthResponse>(baseUrl, "/auth/login", {
    method: "POST",
    body: JSON.stringify({ email, password, captcha_token: captchaToken }),
    headers: lang ? { "Accept-Language": lang } : undefined,
  });
},

resendVerification(baseUrl: string, email: string, captchaToken?: string, lang?: string): Promise<void> {
  return request<void>(baseUrl, "/auth/resend-verification", {
    method: "POST",
    body: JSON.stringify({ email, captcha_token: captchaToken }),
    headers: lang ? { "Accept-Language": lang } : undefined,
  });
},

forgotPassword(baseUrl: string, email: string, captchaToken?: string, lang?: string): Promise<void> {
  return request<void>(baseUrl, "/auth/forgot-password", {
    method: "POST",
    body: JSON.stringify({ email, captcha_token: captchaToken }),
    headers: lang ? { "Accept-Language": lang } : undefined,
  });
},
```

- [ ] **Step 3: Update auth-store.ts to read language and pass it**

Add import at top of `store/auth-store.ts`:

```ts
import { useI18nStore, resolveAcceptLanguage } from "@/store/i18n-store";
```

Update the 4 methods in the store to resolve and pass language:

**login:**
```ts
login: async (email, password, captchaToken) => {
  invalidateRefreshWork();
  const { serverUrl, otpSentAt } = get();
  const lang = resolveAcceptLanguage(useI18nStore.getState().language);
  const resp = await api.login(serverUrl, email, password, captchaToken, lang);
  let newOtpSentAt = otpSentAt;
  if (!resp.user.is_verified) {
    const elapsed = otpSentAt ? (Date.now() - otpSentAt) / 1000 : Infinity;
    if (elapsed >= 60) {
      newOtpSentAt = Date.now();
    }
  }
  set({
    user: resp.user,
    accessToken: resp.access_token,
    refreshToken: resp.refresh_token,
    otpSentAt: newOtpSentAt,
  });
},
```

**register:**
```ts
register: async (name, email, password, captchaToken) => {
  invalidateRefreshWork();
  const { serverUrl } = get();
  const lang = resolveAcceptLanguage(useI18nStore.getState().language);
  const resp = await api.register(serverUrl, name, email, password, captchaToken, lang);
  set({
    user: resp.user,
    accessToken: resp.access_token,
    refreshToken: resp.refresh_token,
    otpSentAt: Date.now(),
  });
},
```

**resendVerification:**
```ts
resendVerification: async (email, captchaToken) => {
  const { serverUrl } = get();
  const lang = resolveAcceptLanguage(useI18nStore.getState().language);
  await api.resendVerification(serverUrl, email, captchaToken, lang);
  set({ otpSentAt: Date.now() });
},
```

**forgotPassword:**
```ts
forgotPassword: async (email, captchaToken) => {
  const { serverUrl } = get();
  const lang = resolveAcceptLanguage(useI18nStore.getState().language);
  await api.forgotPassword(serverUrl, email, captchaToken, lang);
},
```

- [ ] **Step 4: Build check**

```bash
cd /path/to/TabSlate
npx tsc --noEmit
```

Expected: no type errors.

- [ ] **Step 5: Commit**

```bash
git add store/i18n-store.ts lib/api.ts store/auth-store.ts
git commit -m "feat: send Accept-Language header on OTP auth endpoints"
```

---

## Self-Review

**Spec coverage:**
- ✅ Branded HTML template with TabSlate logo and `©2026 TabSlate` footer
- ✅ `Hi {{.Name}},` greeting
- ✅ `{{.Heading}}`, `{{.Intro}}`, `{{.Code}}`, `{{.Note}}` variables
- ✅ `embed.FS` — binary-embedded template
- ✅ `mailer.SendOTP` high-level method — handler has no HTML
- ✅ English + Chinese translations for verify and reset
- ✅ Language passed via `Accept-Language` header → `parseLang`
- ✅ Fallback to "en" for unknown languages
- ✅ `mailer.Send` interface unchanged
- ✅ Chrome extension sends `Accept-Language` on register/login/resend/forgotPassword
- ✅ `resolveAcceptLanguage` maps `"auto"` to `navigator.language`

**Placeholder scan:** None found.

**Type consistency:**
- `otpData` defined in Task 2, used in Task 2 test and Task 3 implementation — consistent
- `otpStrings` defined in Task 3, used only in `translations` map — consistent
- `SendOTP(ctx, to, name, code, purpose, lang string) error` defined in Task 3, called in Task 4 — consistent
- `parseLang(acceptLang string) string` defined in Task 4 step 3, tested in Task 4 step 1 — consistent
- `sendOTPEmail(to, name, code, purpose, lang string)` updated in Task 4 step 5, all 4 call sites updated in Task 4 step 6 — consistent
- `resolveAcceptLanguage(lang: SupportedLanguage): string` defined in Task 6 step 1, imported and called in Task 6 step 3 — consistent
- `api.login/register/resendVerification/forgotPassword` signature extended in Task 6 step 2, callers updated in Task 6 step 3 — consistent
