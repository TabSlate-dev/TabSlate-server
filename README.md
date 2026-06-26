# TabSlate Server

Go backend for the TabSlate Chrome extension. Released under **AGPL-3.0**.

---

## Building

```bash
go build -o tabslate-server ./cmd/server
```

## Deployment

The easiest way to self-host TabSlate Server is using Docker Compose. The official image is automatically built and published to the GitHub Container Registry (`ghcr.io`).

1. Download the `docker-compose.yml` and `.env.example` files:
   ```bash
   curl -O https://raw.githubusercontent.com/TabSlate-dev/TabSlate-server/main/docker-compose.yml
   curl -o .env https://raw.githubusercontent.com/TabSlate-dev/TabSlate-server/main/.env.example
   ```

2. Edit the `.env` file and configure the environment variables (see [Dependencies & Environment Variables](#dependencies--environment-variables)).

3. Start the server:
   ```bash
   docker-compose up -d
   ```

---

## Dependencies & Environment Variables

### Required Dependencies
* **PostgreSQL (17+)**: The primary database. You can use an external provider (like Supabase/Neon) or host your own.
  * `DATABASE_URL`: Your Postgres connection string (`postgres://...`)
* `JWT_SECRET`: A secure random string used to sign access tokens. Generate one using `openssl rand -hex 32`.

### Optional Dependencies
* **Redis**: Used for pub/sub (real-time sync across instances) and rate limiting. Without Redis, the server falls back to in-memory mode, which works perfectly for single-node deployments.
  * `REDIS_URL`: Redis connection string (`redis://...`)
* **Email Provider**: Required if you want users to verify their email addresses or reset passwords via OTP. You can use `smtp`, `resend`, or `ses`.
  * `MAIL_PROVIDER`: Set to your preferred provider. Leave empty to auto-verify all new registrations.
* **MeiliSearch**: Provides powerful full-text search capabilities for bookmarks.
  * `MEILISEARCH_HOST` and `MEILISEARCH_API_KEY`.
* **Prosopo CAPTCHA**: Used for bot protection during registration and login.
  * `PROSOPO_SECRET`.

### Other Configuration
| Variable | Description | Default |
|---|---|---|
| `PORT` | HTTP port the server listens on. | `8080` |
| `GIN_MODE` | Gin framework mode: `release` or `debug`. | `debug` |
| `ALLOW_REGISTRATION` | Set to `false` to disable new user signups. | `true` |

For the complete list of tunable parameters (such as rate limits and specific mail provider configurations), see the [`.env.example`](.env.example) file.

---

## License

TabSlate Server is free software: you can redistribute it and/or modify it
under the terms of the **GNU Affero General Public License** as published by
the Free Software Foundation, either version 3 of the License, or (at your
option) any later version.

> [!WARNING]
> **COMMERCIAL USE RESTRICTION / 禁止商用声明**
> 
> **English**: You may **not** use this software (or any modified version thereof) to operate a paid commercial synchronization service, cloud service, SaaS, or any similar service offered to third parties for a fee, without obtaining a separate commercial license from TabSlate. Free self-hosting for personal use is fully permitted.
> 
> **中文**: 未经 TabSlate 官方单独的商业授权，**严禁**将本软件（或其任何修改版本）用于提供向第三方收费的商业同步服务、云服务或 SaaS。完全允许个人进行免费的私有化部署和内部使用。

See the [LICENSE](LICENSE) file for the full AGPL-3.0 text and the commercial restriction addendum.
