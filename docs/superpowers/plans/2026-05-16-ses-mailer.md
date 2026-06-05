# Amazon SES Mailer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Amazon SES as a third email provider to `internal/mailer`, activated via `MAIL_PROVIDER=ses`, using raw SigV4 signing over stdlib `net/http` with zero new dependencies.

**Architecture:** A new `ses.go` file holds the `sendSES` method and a `signSES` SigV4 helper. `mailer.go` gets four new SES fields on the struct and config, plus a `"ses"` case in the `Send` switch. `app/config.go` and `app/server.go` wire the new env vars through. No call-site changes anywhere else.

**Tech Stack:** Go stdlib only — `crypto/hmac`, `crypto/sha256`, `encoding/hex`, `net/http`, `encoding/json`

**Spec:** `docs/superpowers/specs/2026-05-16-ses-mailer-design.md`

---

## File Map

| Action | File | Responsibility |
|---|---|---|
| Modify | `internal/mailer/mailer.go` | Add SES fields to `Mailer` + `Config`; add `"ses"` case to `Send` switch |
| Create | `internal/mailer/ses.go` | `sendSES` method + `signSES` SigV4 helper |
| Create | `internal/mailer/ses_test.go` | Structural tests for `signSES` output format |
| Modify | `app/config.go` | Add `SESAccessKeyID`, `SESSecretKey`, `SESRegion`, `SESFrom` fields + `os.Getenv` loading |
| Modify | `app/server.go` | Pass SES fields into `mailer.New()` at line 56 |
| Modify | `CLAUDE.md` (server repo) | Document four new env vars in the environment variables table |

---

## Task 1: Add SES fields to `mailer.go`

**Files:**
- Modify: `internal/mailer/mailer.go`

- [ ] **Step 1: Add SES fields to `Mailer` struct**

  In `mailer.go`, the `Mailer` struct currently ends with:
  ```go
  client *http.Client
  ```

  Add four SES fields immediately before `client`:
  ```go
  // SES
  sesAccessKeyID string
  sesSecretKey   string
  sesRegion      string
  sesFrom        string

  client *http.Client
  ```

- [ ] **Step 2: Add SES fields to `Config` struct**

  The `Config` struct currently ends with:
  ```go
  ResendAPIKey string
  ResendFrom   string
  ```

  Add after it:
  ```go
  // SES
  SESAccessKeyID string
  SESSecretKey   string
  SESRegion      string
  SESFrom        string
  ```

- [ ] **Step 3: Wire SES fields in `New()`**

  In `New()`, the return statement currently ends with:
  ```go
  resendAPIKey: cfg.ResendAPIKey,
  resendFrom:   cfg.ResendFrom,
  client: &http.Client{
  ```

  Add after `resendFrom`:
  ```go
  sesAccessKeyID: cfg.SESAccessKeyID,
  sesSecretKey:   cfg.SESSecretKey,
  sesRegion:      cfg.SESRegion,
  sesFrom:        cfg.SESFrom,
  ```

- [ ] **Step 4: Add `"ses"` case to `Send()` switch**

  In `Send()`, the switch currently has:
  ```go
  case "resend":
      return m.sendResend(ctx, to, subject, htmlBody)
  default:
  ```

  Add between `"resend"` and `default`:
  ```go
  case "ses":
      return m.sendSES(ctx, to, subject, htmlBody)
  ```

- [ ] **Step 5: Verify it compiles**

  ```bash
  go build ./internal/mailer/...
  ```

  Expected: compile error — `m.sendSES undefined`. This is correct; `ses.go` hasn't been created yet.

---

## Task 2: Create `ses.go` with SigV4 helper and `sendSES` method

**Files:**
- Create: `internal/mailer/ses.go`

- [ ] **Step 1: Write the failing test first**

  Create `internal/mailer/ses_test.go`:

  ```go
  package mailer

  import (
  	"strings"
  	"testing"
  	"time"
  )

  func TestSignSES_OutputFormat(t *testing.T) {
  	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
  	body := []byte(`{"test":"value"}`)

  	xAmzDate, authorization := signSES("AKID", "SECRET", "us-east-1", body, now)

  	if xAmzDate != "20260516T120000Z" {
  		t.Errorf("xAmzDate = %q, want %q", xAmzDate, "20260516T120000Z")
  	}

  	wantPrefix := "AWS4-HMAC-SHA256 Credential=AKID/20260516/us-east-1/ses/aws4_request,"
  	if !strings.HasPrefix(authorization, wantPrefix) {
  		t.Errorf("authorization = %q\nwant prefix: %q", authorization, wantPrefix)
  	}

  	if !strings.Contains(authorization, "SignedHeaders=content-type;host;x-amz-date,") {
  		t.Errorf("authorization missing SignedHeaders: %q", authorization)
  	}

  	const sigPrefix = "Signature="
  	idx := strings.Index(authorization, sigPrefix)
  	if idx == -1 {
  		t.Fatalf("authorization missing Signature= field: %q", authorization)
  	}
  	sig := authorization[idx+len(sigPrefix):]
  	if len(sig) != 64 {
  		t.Errorf("signature length = %d, want 64 hex chars: %q", len(sig), sig)
  	}
  	for _, c := range sig {
  		if !strings.ContainsRune("0123456789abcdef", c) {
  			t.Errorf("signature contains non-hex char %q: %q", c, sig)
  			break
  		}
  	}
  }
  ```

- [ ] **Step 2: Run the test to verify it fails**

  ```bash
  go test ./internal/mailer/... -run TestSignSES_OutputFormat -v
  ```

  Expected: compile error — `signSES undefined`.

- [ ] **Step 3: Create `ses.go` with the SigV4 helper**

  Create `internal/mailer/ses.go`:

  ```go
  package mailer

  import (
  	"bytes"
  	"context"
  	"crypto/hmac"
  	"crypto/sha256"
  	"encoding/hex"
  	"encoding/json"
  	"fmt"
  	"net/http"
  	"strings"
  	"time"
  )

  // sesEmailRequest is the SES API v2 SendEmail request body.
  type sesEmailRequest struct {
  	FromEmailAddress string         `json:"FromEmailAddress"`
  	Destination      sesDestination `json:"Destination"`
  	Content          sesContent     `json:"Content"`
  }

  type sesDestination struct {
  	ToAddresses []string `json:"ToAddresses"`
  }

  type sesContent struct {
  	Simple sesSimple `json:"Simple"`
  }

  type sesSimple struct {
  	Subject sesCharset `json:"Subject"`
  	Body    sesBody    `json:"Body"`
  }

  type sesCharset struct {
  	Data    string `json:"Data"`
  	Charset string `json:"Charset"`
  }

  type sesBody struct {
  	Html sesCharset `json:"Html"`
  }

  func (m *Mailer) sendSES(ctx context.Context, to, subject, htmlBody string) error {
  	payload, err := json.Marshal(sesEmailRequest{
  		FromEmailAddress: m.sesFrom,
  		Destination:      sesDestination{ToAddresses: []string{to}},
  		Content: sesContent{
  			Simple: sesSimple{
  				Subject: sesCharset{Data: subject, Charset: "UTF-8"},
  				Body:    sesBody{Html: sesCharset{Data: htmlBody, Charset: "UTF-8"}},
  			},
  		},
  	})
  	if err != nil {
  		return fmt.Errorf("ses marshal: %w", err)
  	}

  	url := "https://email." + m.sesRegion + ".amazonaws.com/v2/email/outbound-emails"
  	xAmzDate, authorization := signSES(m.sesAccessKeyID, m.sesSecretKey, m.sesRegion, payload, time.Now().UTC())

  	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
  	if err != nil {
  		return fmt.Errorf("ses request: %w", err)
  	}
  	req.Header.Set("Content-Type", "application/json")
  	req.Header.Set("X-Amz-Date", xAmzDate)
  	req.Header.Set("Authorization", authorization)

  	resp, err := m.client.Do(req)
  	if err != nil {
  		return fmt.Errorf("ses send: %w", err)
  	}
  	defer resp.Body.Close()

  	if resp.StatusCode >= 400 {
  		return fmt.Errorf("ses: unexpected status %d", resp.StatusCode)
  	}
  	return nil
  }

  // signSES produces the X-Amz-Date header value and the AWS4-HMAC-SHA256
  // Authorization header for a SES API v2 SendEmail call.
  // body must be the exact JSON bytes that will be sent as the request body.
  func signSES(accessKeyID, secretKey, region string, body []byte, now time.Time) (xAmzDate, authorization string) {
  	datetime := now.UTC().Format("20060102T150405Z")
  	date := now.UTC().Format("20060102")
  	host := "email." + region + ".amazonaws.com"

  	bodyHash := hexSHA256(body)
  	canonicalHeaders := "content-type:application/json\n" +
  		"host:" + host + "\n" +
  		"x-amz-date:" + datetime + "\n"
  	signedHeaders := "content-type;host;x-amz-date"

  	canonicalRequest := strings.Join([]string{
  		"POST",
  		"/v2/email/outbound-emails",
  		"",
  		canonicalHeaders,
  		signedHeaders,
  		bodyHash,
  	}, "\n")

  	credentialScope := date + "/" + region + "/ses/aws4_request"
  	stringToSign := strings.Join([]string{
  		"AWS4-HMAC-SHA256",
  		datetime,
  		credentialScope,
  		hexSHA256([]byte(canonicalRequest)),
  	}, "\n")

  	kDate := hmacSHA256([]byte("AWS4"+secretKey), date)
  	kRegion := hmacSHA256(kDate, region)
  	kService := hmacSHA256(kRegion, "ses")
  	kSigning := hmacSHA256(kService, "aws4_request")
  	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

  	xAmzDate = datetime
  	authorization = fmt.Sprintf(
  		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
  		accessKeyID, credentialScope, signedHeaders, signature,
  	)
  	return
  }

  func hmacSHA256(key []byte, data string) []byte {
  	h := hmac.New(sha256.New, key)
  	h.Write([]byte(data))
  	return h.Sum(nil)
  }

  func hexSHA256(data []byte) string {
  	sum := sha256.Sum256(data)
  	return hex.EncodeToString(sum[:])
  }
  ```

- [ ] **Step 4: Run the test to verify it passes**

  ```bash
  go test ./internal/mailer/... -run TestSignSES_OutputFormat -v
  ```

  Expected:
  ```
  --- PASS: TestSignSES_OutputFormat (0.00s)
  PASS
  ```

- [ ] **Step 5: Verify full mailer package builds**

  ```bash
  go build ./internal/mailer/...
  ```

  Expected: no output (clean build).

- [ ] **Step 6: Commit**

  ```bash
  git add internal/mailer/ses.go internal/mailer/ses_test.go internal/mailer/mailer.go
  git commit -m "feat: add Amazon SES email provider with SigV4 signing"
  ```

---

## Task 3: Wire SES config through `app/config.go` and `app/server.go`

**Files:**
- Modify: `app/config.go`
- Modify: `app/server.go`

- [ ] **Step 1: Add SES fields to `app/config.go`**

  In `app/config.go`, the `Config` struct has a block that ends with:
  ```go
  ResendAPIKey  string
  ResendFrom    string
  ```

  Add immediately after:
  ```go
  // SES
  SESAccessKeyID string
  SESSecretKey   string
  SESRegion      string
  SESFrom        string
  ```

- [ ] **Step 2: Load SES env vars in `LoadConfig()`**

  In `LoadConfig()`, after the line:
  ```go
  ResendFrom:    os.Getenv("RESEND_FROM"),
  ```

  Add:
  ```go
  SESAccessKeyID: os.Getenv("SES_ACCESS_KEY_ID"),
  SESSecretKey:   os.Getenv("SES_SECRET_KEY"),
  SESRegion:      os.Getenv("SES_REGION"),
  SESFrom:        os.Getenv("SES_FROM"),
  ```

- [ ] **Step 3: Pass SES fields into `mailer.New()` in `app/server.go`**

  In `app/server.go`, the `mailer.New(mailer.Config{...})` call (around line 56) currently ends with:
  ```go
  ResendAPIKey: cfg.ResendAPIKey,
  ResendFrom:   cfg.ResendFrom,
  ```

  Add:
  ```go
  SESAccessKeyID: cfg.SESAccessKeyID,
  SESSecretKey:   cfg.SESSecretKey,
  SESRegion:      cfg.SESRegion,
  SESFrom:        cfg.SESFrom,
  ```

- [ ] **Step 4: Verify the whole project builds**

  ```bash
  go build ./...
  ```

  Expected: no output (clean build).

- [ ] **Step 5: Run all tests**

  ```bash
  go test ./...
  ```

  Expected: all tests pass, no new failures.

- [ ] **Step 6: Commit**

  ```bash
  git add app/config.go app/server.go
  git commit -m "feat: wire SES config fields through app config and server"
  ```

---

## Task 4: Update `CLAUDE.md` documentation

**Files:**
- Modify: `CLAUDE.md` (server repo root)

- [ ] **Step 1: Add SES env vars to the environment variables table**

  In `CLAUDE.md`, find the environment variables table. After the row:
  ```
  | `RESEND_FROM` | | Resend 发件人，如 `TabSlate <noreply@tabslate.com>` |
  ```

  Add four new rows:
  ```
  | `SES_ACCESS_KEY_ID` | | AWS access key ID（`MAIL_PROVIDER=ses` 时必填） |
  | `SES_SECRET_KEY` | | AWS secret access key（`MAIL_PROVIDER=ses` 时必填） |
  | `SES_REGION` | | SES 所在 AWS region，如 `us-east-1`（`MAIL_PROVIDER=ses` 时必填） |
  | `SES_FROM` | | SES 发件人，如 `TabSlate <noreply@tabslate.com>`（`MAIL_PROVIDER=ses` 时必填） |
  ```

  Also update the `MAIL_PROVIDER` row description from:
  ```
  | `MAIL_PROVIDER` | | `smtp` 或 `resend`；空 = 禁用邮件（用户自动验证） |
  ```
  to:
  ```
  | `MAIL_PROVIDER` | | `smtp`、`resend` 或 `ses`；空 = 禁用邮件（用户自动验证） |
  ```

  Also update the `internal/mailer` row in the package responsibilities table from:
  ```
  | `internal/mailer` | 内部 | 邮件发送，支持 SMTP 和 Resend 两种后端；`MAIL_PROVIDER` 为空则禁用（用户自动验证） |
  ```
  to:
  ```
  | `internal/mailer` | 内部 | 邮件发送，支持 SMTP、Resend 和 Amazon SES 三种后端；`MAIL_PROVIDER` 为空则禁用（用户自动验证） |
  ```

- [ ] **Step 2: Verify build still passes**

  ```bash
  go build ./...
  ```

  Expected: no output.

- [ ] **Step 3: Commit**

  ```bash
  git add CLAUDE.md
  git commit -m "docs: document Amazon SES env vars and update mailer description"
  ```

---

## Done

All four tasks complete. Verify the full state:

```bash
go build ./...
go test ./...
```

Both must pass cleanly. The feature is activated by setting:
```
MAIL_PROVIDER=ses
SES_ACCESS_KEY_ID=<your-key-id>
SES_SECRET_KEY=<your-secret>
SES_REGION=us-east-1
SES_FROM=TabSlate <noreply@tabslate.com>
```
