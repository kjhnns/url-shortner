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

- `AUTH_MODE=password` (the only supported mode today): one shared password. The
  server stores **only its SHA-256 hash** (`APP_PASSWORD_HASH`, lowercase hex),
  never the plaintext. `/auth/login` SHA-256s the submitted plaintext (inside TLS)
  and constant-time compares it to the hash, then sets an HMAC-signed session
  cookie (`crypto/hmac`, secret from `SESSION_SECRET` or an ephemeral one if
  unset). `/auth/logout` clears it. The app **refuses to start** if
  `APP_PASSWORD_HASH` is unset, so it never runs wide open.

  Derive the hash (raw bytes, no trailing newline, lowercase hex):

  ```sh
  printf '%s' '<the app password>' | shasum -a 256   # -> APP_PASSWORD_HASH
  ```

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
| `APP_PASSWORD_HASH` | _(required)_ | lowercase hex SHA-256 of the app password; app won't start without it. Gates both the web UI and the JSON API (one secret, only its hash at rest). |
| `SESSION_SECRET` | _(generated)_ | set to a long random string to persist sessions across restarts |

## Run locally (password mode)

```sh
# Hash the password once; the server only ever sees the hash.
export APP_PASSWORD_HASH=$(printf '%s' 'secret' | shasum -a 256 | awk '{print $1}')
APP_PASSWORD_HASH=$APP_PASSWORD_HASH PORT=8080 go run .
# then open http://localhost:8080/admin and sign in with "secret"
```

Build a binary:

```sh
go build -o url-shortner .
APP_PASSWORD_HASH=$(printf '%s' 'secret' | shasum -a 256 | awk '{print $1}') ./url-shortner
```

## Root redirect (configurable)

`gojoe.run/` (the root) is configurable from the admin panel. In `/admin` there is
a **Root redirect** field: set an absolute http(s) URL and `/` will `302` there
(public, no auth to follow). Leave it empty (or hit **Clear**) to fall back to the
minimal landing page. `/` is always a reserved path, never a normal slug.

It is also exposed over the API:

- `GET /api/settings/root_redirect` -> `{"target":"..."}` (`""` if unset)
- `PUT /api/settings/root_redirect` body `{"target":"https://..."}` (empty target clears it)

## JSON API

A minimal, plain REST API. **There is one secret total** (the app password) and
no separate API token. The web UI uses the session cookie; the API uses a Bearer
token whose value is the **SHA-256 hash of the password** (lowercase hex), so the
raw password never rides the API and the server only ever holds the hash
(`APP_PASSWORD_HASH`). Both surfaces gate the same link operations.

- Every request must send `Authorization: Bearer <sha256-hex-of-password>` or it
  is rejected with `401`. (`APP_PASSWORD_HASH` is required for the app to start,
  so the API is always enabled.)
- Errors are a simple JSON envelope: `{"error":"..."}`.

| Method & path | Body | Result |
| --- | --- | --- |
| `POST /api/links` | `{"slug":"...","target":"..."}` | `201` with the link; `409` if the slug already exists |
| `GET /api/links` | _(none)_ | `200` JSON array: `slug, target, clicks, created_at, updated_at` |
| `GET /api/links/{slug}` | _(none)_ | `200` the link; `404` if absent |
| `PUT /api/links/{slug}` | `{"target":"..."}` | `200` the updated link; `404` if absent |
| `DELETE /api/links/{slug}` | _(none)_ | `200` `{"status":"deleted","slug":"..."}`; `404` if absent |

Validation is shared with the web UI: slug `^[A-Za-z0-9_-]{1,64}$` (and not a
reserved path), target must be an absolute `http(s)` URL. Invalid input returns
`400`. `POST` is create-only (no upsert): use `PUT` to change an existing target.

```sh
# The bearer token is the SHA-256 of the app password (passctl gojoe/app-password):
PW=$(printf '%s' "$(passctl get gojoe/app-password)" | shasum -a 256 | awk '{print $1}')

# create
curl -X POST https://gojoe.run/api/links \
  -H "Authorization: Bearer $PW" -H "Content-Type: application/json" \
  -d '{"slug":"wishlist","target":"https://kjhnns.github.io/agent-workspace/wishlist.html"}'

# list / get / update / delete
curl https://gojoe.run/api/links            -H "Authorization: Bearer $PW"
curl https://gojoe.run/api/links/wishlist   -H "Authorization: Bearer $PW"
curl -X PUT https://gojoe.run/api/links/wishlist \
  -H "Authorization: Bearer $PW" -H "Content-Type: application/json" \
  -d '{"target":"https://example.com/new"}'
curl -X DELETE https://gojoe.run/api/links/wishlist -H "Authorization: Bearer $PW"
```

## CLI (`shortcli`)

A dependency-free Go client for the API, in the same module under `cmd/shortcli`.

```sh
go build -o shortcli ./cmd/shortcli
export GOJOE_BASE_URL=https://gojoe.run     # default; override for local testing
export GOJOE_PASSWORD='<the app password>'  # plaintext; CLI hashes it itself
# (or omit GOJOE_PASSWORD and let the CLI read passctl gojoe/app-password)

shortcli create <slug> <url>
shortcli list
shortcli get <slug>
shortcli update <slug> <url>
shortcli delete <slug>
```

It reads the base URL from `GOJOE_BASE_URL` (default `https://gojoe.run`) and the
plaintext app password from `GOJOE_PASSWORD` (or, if unset, from `passctl
gojoe/app-password`), then sends its **SHA-256 hash** as the Bearer token. There
is no separate API token. Output is plain text; errors print the server's
`{"error":...}` message and exit non-zero.

## Seeding existing links

`deploy/seed-links.txt` lists Joe's existing public pages (`slug url` per line).
After the API is live (see `DEPLOY.md`), create them all in one loop:

```sh
while read -r slug url; do
  [ -z "$slug" ] && continue
  case "$slug" in \#*) continue ;; esac
  shortcli create "$slug" "$url"
done < deploy/seed-links.txt
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
admin-list -> update -> delete lifecycle, plus slug/URL validation. Also covers
the JSON API (create/list/get/update/delete happy paths, `401` without/with the
wrong bearer token, `404` for a missing slug, `400` validation) and the
configurable root redirect (set -> `302`, clear -> landing `200`).

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

See `deploy/` for the example systemd unit and Caddyfile, and **`DEPLOY.md`** for
a precise, copy-pasteable runbook (initial deploy, redeploy-after-change, turning
on the API, verification, and seeding).
