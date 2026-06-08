# Email Template Support Design

**Date:** 2026-06-01  
**Repos:** TabSlate-server, TabSlate (Chrome extension)  
**Status:** Approved

## Overview

Replace the inline `fmt.Sprintf` HTML in `sendOTPEmail` with a proper HTML template system. The `mailer` package owns the template file, renders it, and exposes a high-level `SendOTP` method. The handler no longer touches HTML. The Chrome extension explicitly sends `Accept-Language` so the server can select the correct language.

## Goals

- Use a user-provided branded HTML template instead of bare inline HTML
- Support English and Chinese (i18n strings selected by `lang` parameter)
- Embed the template in the binary via `embed.FS` (no external files at runtime)
- Keep `mailer.Send` interface unchanged; add `SendOTP` as the new high-level call
- Chrome extension sends `Accept-Language` header on the 4 OTP-triggering endpoints

## Non-Goals

- New email types (only OTP emails are in scope)
- Dynamic template loading without recompile
- Language auto-detection (caller is responsible for passing `lang`)

---

## Architecture

### Method A — Template rendering in `mailer` package

```
handler.sendOTPEmail(to, name, code, purpose, lang)
    └── h.mailer.SendOTP(ctx, to, name, code, purpose, lang)
            ├── lookup translations[purpose][lang]  (fallback: "en")
            ├── tmpl.Execute(&buf, otpData{...})
            └── m.Send(ctx, to, subject, buf.String())
```

The handler has zero HTML knowledge. All template + copy logic lives in `mailer`.

---

## File Changes

### New: `internal/mailer/templates/otp.html`

The `template.html` in the repo root, updated to:

**Brand substitution (static HTML edits):**
- Both `<img>` logo elements → `<img src="https://tabslate.com/logo.svg" width="32" height="32" alt="TabSlate">` + adjacent `<span style="font-size:20px;font-weight:700;">TabSlate</span>`
- Footer links → TabSlate equivalents (or remove)
- Copyright → `©2026 TabSlate`

**Go template variables injected:**

| Location in HTML | Variable | Notes |
|---|---|---|
| Top of body (new) | `Hi {{.Name}},` | Greeting line before h1 |
| h1 content | `{{.Heading}}` | e.g. "Confirm your email address" |
| Intro paragraph | `{{.Intro}}` | Descriptive text |
| OTP code display | `{{.Code}}` | 6-digit code |
| Footer note | `{{.Note}}` | Ignore/safety message |

Template engine: `html/template` (auto-escapes injected values).

### Modified: `internal/mailer/mailer.go`

**Additions:**

```go
//go:embed templates/*.html
var tmplFS embed.FS

type otpData struct {
    Name, Heading, Intro, Code, Note string
}

type otpStrings struct {
    Subject, Heading, Intro, Note string
}
```

**Translation map** (package-level var, English + Chinese):

```
translations["verify"]["en"] = {
    Subject: "Verify your TabSlate email",
    Heading: "Confirm your email address",
    Intro:   "Enter the code below to verify your email. It expires in 10 minutes.",
    Note:    "If you didn't create an account, you can safely ignore this email.",
}
translations["verify"]["zh"] = {
    Subject: "验证您的 TabSlate 邮箱",
    Heading: "确认您的邮箱地址",
    Intro:   "请在下方输入验证码完成邮箱验证，验证码 10 分钟内有效。",
    Note:    "如果您没有注册账号，请忽略此邮件。",
}
translations["reset"]["en"] = {
    Subject: "Reset your TabSlate password",
    Heading: "Reset your password",
    Intro:   "Enter the code below to reset your password. It expires in 10 minutes.",
    Note:    "If you didn't request a password reset, you can safely ignore this email.",
}
translations["reset"]["zh"] = {
    Subject: "重置您的 TabSlate 密码",
    Heading: "重置密码",
    Intro:   "请在下方输入验证码以重置密码，验证码 10 分钟内有效。",
    Note:    "如果您没有申请重置密码，请忽略此邮件。",
}
```

**`Mailer` struct change:** Add `tmpl *template.Template` field.

**`New()` change:** Parse template at construction time:
```go
m.tmpl = template.Must(template.ParseFS(tmplFS, "templates/otp.html"))
```
Panics on malformed template — acceptable as programming error, not runtime error.

**New method:**
```go
func (m *Mailer) SendOTP(ctx context.Context, to, name, code, purpose, lang string) error
```
- Looks up `translations[purpose][lang]`; falls back to `translations[purpose]["en"]` if `lang` not found
- Renders `tmpl` with `otpData{Name: name, Heading: s.Heading, Intro: s.Intro, Code: code, Note: s.Note}`
- Calls `m.Send(ctx, to, s.Subject, buf.String())`
- Returns render or send errors

### Modified: `internal/handler/auth.go`

**New helper:**
```go
func parseLang(acceptLang string) string {
    if strings.Contains(acceptLang, "zh") {
        return "zh"
    }
    return "en"
}
```

**`sendOTPEmail` signature change:**
```go
func (h *AuthHandler) sendOTPEmail(to, name, code, purpose, lang string) {
    if err := h.mailer.SendOTP(context.Background(), to, name, code, purpose, lang); err != nil {
        log.Printf("failed to send OTP email to %s: %v", to, err)
    }
}
```

**Four call sites** (lines 178, 261, 399, 458) updated to:
```go
go h.sendOTPEmail(email, name, otp, "verify", parseLang(c.GetHeader("Accept-Language")))
go h.sendOTPEmail(email, name, otp, "reset",  parseLang(c.GetHeader("Accept-Language")))
```

---

## Error Handling

| Scenario | Behavior |
|---|---|
| Template parse fails at startup | `template.Must` panics — crash fast, fix the template |
| Template render fails at runtime | `SendOTP` returns error → `sendOTPEmail` logs and returns |
| `lang` not in translation map | Falls back to `"en"` silently |
| `purpose` not in translation map | Falls back to `translations["verify"]["en"]` |

---

## Testing

- Unit test `SendOTP` with a mock/disabled mailer (`provider = ""`): confirm template renders without error for all purpose × lang combinations
- Verify `parseLang` correctly maps `"zh-CN"`, `"zh-TW"`, `"en-US"`, `""` to expected codes

---

## Frontend Changes (TabSlate Chrome Extension)

### `store/i18n-store.ts`

Add exported helper:
```ts
export function resolveAcceptLanguage(lang: SupportedLanguage): string {
  if (lang === "zh_CN") return "zh-CN";
  if (lang === "en") return "en";
  return navigator.language; // "auto" → browser locale (e.g. "zh-CN", "en-US")
}
```

### `lib/api.ts`

Add `lang?: string` parameter to `register`, `login`, `resendVerification`, `forgotPassword`. Pass `Accept-Language` header when provided:

```ts
register(baseUrl, name, email, password, captchaToken, lang): Promise<AuthResponse> {
  return request(baseUrl, "/auth/register", {
    method: "POST",
    body: JSON.stringify({ name, email, password, captcha_token: captchaToken }),
    headers: lang ? { "Accept-Language": lang } : undefined,
  });
}
```
Same pattern for the other three functions.

### `store/auth-store.ts`

Import `useI18nStore` and `resolveAcceptLanguage`. In each of the 4 methods, read and resolve the language before calling api:

```ts
login: async (email, password, captchaToken) => {
  const lang = resolveAcceptLanguage(useI18nStore.getState().language);
  const resp = await api.login(serverUrl, email, password, captchaToken, lang);
  // ... rest unchanged
},
```

---

## Migration Notes

- `template.html` in repo root can be deleted after `otp.html` is created
- No DB changes, no env var changes
- `mailer.Send` signature is unchanged — other callers unaffected
- Chrome extension changes are backwards-compatible: server already handles missing `Accept-Language` by defaulting to "en"
