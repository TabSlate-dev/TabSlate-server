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

> [!WARNING]
> **COMMERCIAL USE RESTRICTION / 禁止商用声明**
> 
> **English**: You may **not** use this software (or any modified version thereof) to operate a paid commercial synchronization service, cloud service, SaaS, or any similar service offered to third parties for a fee, without obtaining a separate commercial license from TabSlate. Free self-hosting for personal or internal business use is fully permitted.
> 
> **中文**: 未经 TabSlate 官方单独的商业授权，**严禁**将本软件（或其任何修改版本）用于提供向第三方收费的商业同步服务、云服务或 SaaS。完全允许个人或企业进行免费的私有化部署和内部使用。

See the [LICENSE](LICENSE) file for the full AGPL-3.0 text and the commercial restriction addendum.
