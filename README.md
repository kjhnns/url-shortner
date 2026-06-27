# url-shortner (gojoe.run)

A small, vanilla Go + SQLite URL shortener with **claim-on-visit** slugs and a
mobile-friendly self-service dashboard. Built on the standard library
(`net/http`, `html/template`) with exactly one external dependency:
`modernc.org/sqlite` (pure Go, no cgo).

## What it does

- `gojoe.run/<slug>` -> if the slug exists, **302 redirect** to its target URL
  (public, open to everyone; click count + last-visited are recorded).
- If the slug does **not** exist, you get a **claim page**: paste a URL, hit Save,
  and `slug -> target` is created. (Claim-on-visit. The claim page is protected.)
- `gojoe.run/admin` -> list all links with **edit** (change target) and **delete**.
  Mobile-friendly.
- `gojoe.run/healthz` -> `200 OK` for uptime checks (public).

Reserved top-level paths that are never treated as slugs: `/admin`, `/auth`,
`/static`, `/healthz`, and `/` (the landing page).

## Access control

The **shipped version is password-only.** Public redirects are open to everyone;
the claim/save page and all of `/admin` sit behind a single shared password.

- `AUTH_MODE=password` (the only supported mode today): one shared password
  (`APP_PASSWORD`). `/auth/login` checks it and sets an HMAC-signed session
  cookie (`crypto/hmac`, secret from `SESSION_SECRET` or an ephemeral one if
  unset). `/auth/logout` clears it. The app **refuses to start** if
  `APP_PASSWORD` is unset, so it never runs wide open.

The auth layer is a single middleware (`Auth.Middleware`) wrapping the protected
routes, so a richer mode can be added later without restructuring.

### Future: access control options

When more than a shared secret is wanted, two paths fit cleanly behind the same
middleware (not shipped yet):

- **In-app Google OAuth + email allowlist** (recommended next step): Google
  login, allowlist of emails managed in `/admin` by the owner. One-time setup of
  a Google Cloud OAuth client (type *Web application*, redirect URI
  `https://gojoe.run/auth/callback`, copy the client id + secret into env). Adds
  no extra Go dependency (stdlib `net/http` + `crypto/hmac` only).
- **Cloudflare Access** in front of the origin (not used here: gojoe.run runs on
  a Hetzner VPS behind Caddy with Namecheap DNS, no Cloudflare).

## Configuration (all via env)

| Var | Default | Notes |
| --- | --- | --- |
| `PORT` | `8080` | listen port |
| `DATABASE_PATH` | `./shortener.db` | SQLite file (WAL mode) |
| `BASE_URL` | _(empty)_ | e.g. `https://gojoe.run`; enables Secure cookies |
| `AUTH_MODE` | `password` | only `password` supported |
| `APP_PASSWORD` | _(required)_ | shared password; app won't start without it |
| `SESSION_SECRET` | _(generated)_ | set to a long random string to persist sessions across restarts |

## Run locally (password mode)

```sh
APP_PASSWORD=secret PORT=8080 go run .
# then open http://localhost:8080/admin and sign in with "secret"
```

Build a binary:

```sh
go build -o url-shortner .
APP_PASSWORD=secret ./url-shortner
```

## Schema

Created on startup with `CREATE TABLE IF NOT EXISTS` (no migration library):

```sql
links(
  slug TEXT PRIMARY KEY,
  target_url TEXT NOT NULL,
  created_at TEXT, updated_at TEXT, created_by TEXT,
  clicks INTEGER DEFAULT 0, last_visited_at TEXT
)
```

SQLite is opened with WAL + `busy_timeout` pragmas and `SetMaxOpenConns(1)`
(single writer).

## Tests

```sh
go vet ./...
go test ./...
```

Covers: healthz, protected routes rejecting unauthenticated requests, the claim
page for unknown slugs (authed), and the full create -> 302 redirect ->
admin-list -> update -> delete lifecycle, plus slug/URL validation.

## Deployment (Hetzner box, no Docker)

DNS: point gojoe.run's A/AAAA records (Namecheap) at the Hetzner box.

1. Build for the server and copy the binary to `/usr/local/bin/url-shortner`.
2. Create `/var/lib/url-shortner` owned by the service user.
3. Put secrets in `/etc/url-shortner.env` (`chmod 600`).
4. Install `deploy/url-shortner.service` and enable it (`Restart=always`).
5. Install `deploy/Caddyfile`; Caddy reverse-proxies gojoe.run to `127.0.0.1:8080`
   with automatic HTTPS.

Backups: run **Litestream** against `DATABASE_PATH` to stream the SQLite DB to
object storage (e.g. an S3-compatible bucket) for point-in-time recovery.

See `deploy/` for the example systemd unit and Caddyfile.
