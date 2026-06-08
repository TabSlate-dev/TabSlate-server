# TabSlate Server

Go backend for the TabSlate Chrome extension. Released under **AGPL-3.0**.

---

## Building

```bash
go build -o tabslate-server ./cmd/server
```

Docker:

```bash
docker build -t tabslate-server .
```

---

## Runtime Environment Variables

| Variable | Required | Description |
|---|---|---|
| `DATABASE_URL` | ✅ | PostgreSQL DSN (`postgres://...`) |
| `JWT_SECRET` | ✅ | HMAC secret for access tokens |
| `PORT` | | HTTP port (default `8080`) |
| `GIN_MODE` | | Gin mode: `release` / `debug` (default `debug`) |
| `ALLOW_REGISTRATION` | | Set to `false` to disable new user registration (default `true`) |
| `PROSOPO_SECRET` | | Captcha secret; omit to disable captcha |
| `MAIL_PROVIDER` | | `smtp` / `resend` / `ses`; omit to auto-verify all registrations |

See `.env.example` for the full list including SMTP, Resend, SES, and rate-limit tunables.

---

## License

TabSlate Server is free software: you can redistribute it and/or modify it
under the terms of the **GNU Affero General Public License** as published by
the Free Software Foundation, either version 3 of the License, or (at your
option) any later version.

You may **not** use this software to operate a paid commercial synchronization
service for third parties without a separate commercial license from TabSlate.
See LICENSE for details.
