# Amazon SES Mailer — Design Spec

**Date:** 2026-05-16
**Status:** Approved

## Overview

Add Amazon SES as a third email provider option to `internal/mailer`, alongside the existing `smtp` and `resend` backends. Activated by setting `MAIL_PROVIDER=ses`. Uses raw SigV4 signing over `net/http` — no AWS SDK dependency.

## Scope

Available in the OSS server (`TabSlate-server`). Cloud inherits it automatically since it imports this repo as a Go module dependency.

## Architecture

### Files Changed

| File | Change |
|---|---|
| `internal/mailer/mailer.go` | Add SES fields to `Mailer` struct and `Config`; add `"ses"` case to `Send` switch |
| `internal/mailer/ses.go` | New — `sendSES` method + package-level `signSES` SigV4 helper |
| `app/config.go` | Add `SESAccessKeyID`, `SESSecretKey`, `SESRegion`, `SESFrom` fields + `os.Getenv` loading |
| `CLAUDE.md` (server repo) | Document new env vars |

No changes to handlers, auth flow, or the `mailer.Send(ctx, to, subject, html)` signature. All existing callers are unaffected.

## Config & Env Vars

Four new env vars, all `SES_`-namespaced:

| Env Var | Required | Description |
|---|---|---|
| `MAIL_PROVIDER=ses` | ✅ | Selects SES backend |
| `SES_ACCESS_KEY_ID` | ✅ | AWS access key ID |
| `SES_SECRET_KEY` | ✅ | AWS secret access key |
| `SES_REGION` | ✅ | AWS region, e.g. `us-east-1` |
| `SES_FROM` | ✅ | Sender address, e.g. `TabSlate <noreply@tabslate.com>` |

No startup validation — missing credentials surface as a send-time error, consistent with the existing SMTP and Resend behavior.

`app/Config` additions:

```go
SESAccessKeyID string
SESSecretKey   string
SESRegion      string
SESFrom        string
```

Loaded in `LoadConfig()` via `os.Getenv("SES_ACCESS_KEY_ID")` etc., following the same pattern as `ResendAPIKey` / `ResendFrom`.

## SES HTTP Call (`ses.go`)

### Endpoint

```
POST https://email.{region}.amazonaws.com/v2/email/outbound-emails
```

### Request Body

```json
{
  "FromEmailAddress": "<SES_FROM>",
  "Destination": { "ToAddresses": ["<to>"] },
  "Content": {
    "Simple": {
      "Subject": { "Data": "<subject>", "Charset": "UTF-8" },
      "Body":    { "Html":    { "Data": "<htmlBody>", "Charset": "UTF-8" } }
    }
  }
}
```

### Required Headers

| Header | Value |
|---|---|
| `Content-Type` | `application/json` |
| `X-Amz-Date` | ISO8601 datetime, e.g. `20260516T120000Z` |
| `Authorization` | AWS4-HMAC-SHA256 SigV4 header (see below) |

### SigV4 Signing

Implemented as a package-level unexported function in `ses.go`:

```go
func signSES(accessKeyID, secretKey, region string, body []byte, now time.Time) (xAmzDate, authorization string)
```

Four-step algorithm using only `crypto/hmac`, `crypto/sha256`, `encoding/hex`, `strings`, and `time` from stdlib:

1. **Canonical request**
   ```
   POST
   /v2/email/outbound-emails

   content-type:application/json
   host:email.<region>.amazonaws.com
   x-amz-date:<datetime>

   content-type;host;x-amz-date
   <hex(sha256(body))>
   ```

2. **String to sign**
   ```
   AWS4-HMAC-SHA256
   <datetime>
   <date>/<region>/ses/aws4_request
   <hex(sha256(canonicalRequest))>
   ```

3. **Signing key** — HMAC derivation chain:
   ```
   kDate    = HMAC("AWS4" + secretKey, date)
   kRegion  = HMAC(kDate, region)
   kService = HMAC(kRegion, "ses")
   kSigning = HMAC(kService, "aws4_request")
   ```

4. **Authorization header**
   ```
   AWS4-HMAC-SHA256 Credential=<keyId>/<date>/<region>/ses/aws4_request, SignedHeaders=content-type;host;x-amz-date, Signature=<hex(HMAC(kSigning, stringToSign))>
   ```

### Error Handling

Mirrors `sendResend`:
- HTTP transport errors: `fmt.Errorf("ses send: %w", err)`
- Non-2xx status: `fmt.Errorf("ses: unexpected status %d", resp.StatusCode)`

Uses the existing shared `*http.Client` (15s timeout) from `Mailer`.

## What Is Not Changing

- `mailer.Send(ctx, to, subject, html string) error` — signature unchanged
- All auth handler call sites — unaffected
- `go.mod` — no new dependencies
- Boot-time behavior — mailer still disabled (no-op) when `MAIL_PROVIDER` is empty

## Testing

No unit tests for the SigV4 helper. The function accepts `time.Time` making isolated testing possible, but the logic is pure stdlib HMAC glue with no meaningful branching. Integration testing against an SES sandbox endpoint is the correct validation path and is out of scope for CI.

Build verification: `go build ./...` must pass with no new deps in `go.mod`.
