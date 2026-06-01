# TabSlate Server

Go backend for the TabSlate self-hosted (OSS) edition. Ships as a compiled binary and Docker image; source is not redistributable.

---

## Building

### Standard build

```bash
go build -o tabslate-server ./cmd/server
```

### Production build (with keygen.sh license enforcement)

`KEYGEN_API_URL` and `KEYGEN_ACCOUNT_ID` are baked into the binary at compile time to prevent runtime redirection to a fake license endpoint.

```bash
go build \
  -ldflags "-X 'github.com/tabslate/server/billing/local.KeygenAPIURL=https://api.keygen.sh' \
            -X 'github.com/tabslate/server/billing/local.KeygenAccountID=<ACCOUNT_ID>'" \
  -o tabslate-server \
  ./cmd/server
```

Replace `<ACCOUNT_ID>` with the TabSlate keygen.sh account ID.

### Docker build

```bash
docker build \
  --build-arg KEYGEN_API_URL=https://api.keygen.sh \
  --build-arg KEYGEN_ACCOUNT_ID=<ACCOUNT_ID> \
  -t tabslate-server .
```

Ensure the `Dockerfile` passes these as `-ldflags` to `go build`:

```dockerfile
ARG KEYGEN_API_URL
ARG KEYGEN_ACCOUNT_ID
RUN go build \
      -ldflags "-X 'github.com/tabslate/server/billing/local.KeygenAPIURL=${KEYGEN_API_URL}' \
                -X 'github.com/tabslate/server/billing/local.KeygenAccountID=${KEYGEN_ACCOUNT_ID}'" \
      -o /tabslate-server ./cmd/server
```

---

## Runtime Environment Variables

| Variable | Required | Description |
|---|---|---|
| `DATABASE_URL` | ✅ | PostgreSQL DSN (`postgres://...`) |
| `JWT_SECRET` | ✅ | HMAC secret for access tokens |
| `PORT` | | HTTP port (default `8080`) |
| `GIN_MODE` | | Gin mode: `release` / `debug` (default `debug`) |
| `KEYGEN_LICENSE_KEY` | | keygen.sh license key; omit for Free tier (3 users max) |
| `PROSOPO_SECRET` | | Captcha secret; omit to disable captcha |
| `MAIL_PROVIDER` | | `smtp` / `resend` / `ses`; omit to auto-verify all registrations |

See `.env.example` for the full list including SMTP, Resend, SES, and rate-limit tunables.

---

## License

TabSlate Server is proprietary software. Redistribution and modification are not permitted. See LICENSE for details.
