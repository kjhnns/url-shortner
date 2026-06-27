# DEPLOY.md — gojoe.run runbook (Hetzner VPS, Debian)

Operational runbook for deploying / redeploying the URL shortener on the Hetzner
box that serves `gojoe.run`. Written to be executed step by step by an operator
(human or agent) **on the VPS**. Commands are copy-pasteable; run them in order.

Canonical paths and names (must stay consistent with `deploy/`):

| Thing | Value |
| --- | --- |
| service name | `url-shortner` |
| binary | `/usr/local/bin/url-shortner` |
| repo checkout | `/opt/url-shortner` |
| runtime data dir | `/var/lib/url-shortner` |
| SQLite DB | `/var/lib/url-shortner/shortener.db` |
| env file | `/etc/url-shortner.env` (chmod 600, NOT in git) |
| listen port | `8080` (Caddy reverse-proxies `gojoe.run` -> `127.0.0.1:8080`) |
| service user | `urlshort` |

## SECURITY — read first

Real secrets must NEVER be committed to this repo (it is public on GitHub). There
is exactly ONE auth secret: the app password (`APP_PASSWORD`). It also serves as
the JSON API's Bearer token, so there is NO separate API token. It lives ONLY in:

1. the operator's passctl: `gojoe/app-password`, and
2. the runtime env file `/etc/url-shortner.env`, which is `chmod 600` and is NOT
   tracked by git.

Everywhere in this repo you see `<set-from-passctl ...>` it is a PLACEHOLDER. The
operator substitutes the real value out of band when writing `/etc/url-shortner.env`.

---

## 0. Prerequisites

```sh
# Debian 12 (bookworm). Run as a sudo-capable user.
sudo apt-get update
sudo apt-get install -y git curl

# Go toolchain (1.25+). If `go version` already prints >= 1.25, skip this.
go version || {
  GO_VER=1.25.5
  curl -fsSL "https://go.dev/dl/go${GO_VER}.linux-amd64.tar.gz" -o /tmp/go.tgz
  sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf /tmp/go.tgz
  echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh
  export PATH=$PATH:/usr/local/go/bin
  go version
}

# Caddy (reverse proxy + automatic HTTPS).
caddy version || {
  sudo apt-get install -y debian-keyring debian-archive-keyring apt-transport-https
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
    | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
  curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
    | sudo tee /etc/apt/sources.list.d/caddy-stable.list
  sudo apt-get update && sudo apt-get install -y caddy
}
```

DNS: `gojoe.run` A/AAAA records (Namecheap) must already point at this box so
Caddy can obtain a Let's Encrypt certificate.

---

## 1. Initial deploy (first time only)

```sh
# 1a. Service user + data dir.
sudo useradd --system --home /var/lib/url-shortner --shell /usr/sbin/nologin urlshort || true
sudo mkdir -p /var/lib/url-shortner
sudo chown urlshort:urlshort /var/lib/url-shortner

# 1b. Clone the repo (the API + CLI live on the feat/shortener branch / merged main).
sudo git clone https://github.com/kjhnns/url-shortner.git /opt/url-shortner
cd /opt/url-shortner
sudo git checkout main      # or: sudo git checkout feat/shortener

# 1c. Build the server binary.
sudo /usr/local/go/bin/go build -o /usr/local/bin/url-shortner .

# 1d. Runtime env file. Substitute the <...> placeholders with the REAL values
#     from passctl. DO NOT commit this file (it is /etc, not the repo).
sudo tee /etc/url-shortner.env >/dev/null <<'EOF'
AUTH_MODE=password
APP_PASSWORD=<set-from-passctl gojoe/app-password>
SESSION_SECRET=<long-random-string, generate with: openssl rand -hex 32>
PORT=8080
DATABASE_PATH=/var/lib/url-shortner/shortener.db
BASE_URL=https://gojoe.run
EOF
sudo chmod 600 /etc/url-shortner.env
sudo chown root:root /etc/url-shortner.env

# 1e. Install + enable the systemd unit (from deploy/).
sudo cp /opt/url-shortner/deploy/url-shortner.service /etc/systemd/system/url-shortner.service
sudo systemctl daemon-reload
sudo systemctl enable --now url-shortner
sudo systemctl status url-shortner --no-pager

# 1f. Install + reload Caddy (from deploy/).
sudo cp /opt/url-shortner/deploy/Caddyfile /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

Now jump to **section 5 (Verification)**.

---

## 2. Redeploy after a code change  ← the immediate need (turns ON the API)

The service is already live on the pre-API build. The new JSON API authenticates
with the EXISTING `APP_PASSWORD` (no new env var to set), so turning it on is just
a redeploy:

```sh
cd /opt/url-shortner
sudo git pull --ff-only
sudo /usr/local/go/bin/go build -o /usr/local/bin/url-shortner .
sudo systemctl restart url-shortner
sudo systemctl status url-shortner --no-pager
```

Then verify (section 3). The API is enabled automatically because `APP_PASSWORD`
is already set in `/etc/url-shortner.env` (it is required for the app to start).

---

## 3. Verification

```sh
# 3a. Local health: expect HTTP 200 / body "ok".
curl -s -o /dev/null -w '%{http_code}\n' http://localhost:8080/healthz

# 3b. Public health through Caddy + HTTPS: expect HTTP/2 200.
curl -sI https://gojoe.run/healthz | head -1

# 3c. API round-trip. The bearer token IS the app password; read it from the env
#     file on the box (it is not in git):
TOKEN=$(sudo sed -n 's/^APP_PASSWORD=//p' /etc/url-shortner.env)

# create a throwaway test link -> expect 201
curl -s -w '\n%{http_code}\n' -X POST https://gojoe.run/api/links \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"slug":"deploytest","target":"https://example.com/deploytest"}'

# confirm the public redirect resolves -> expect 302 and Location: https://example.com/deploytest
curl -s -o /dev/null -w 'redirect=%{http_code} loc=%header{location}\n' https://gojoe.run/deploytest

# clean up the test link -> expect 200
curl -s -w '\n%{http_code}\n' -X DELETE https://gojoe.run/api/links/deploytest \
  -H "Authorization: Bearer $TOKEN"

# confirm a missing/invalid token is rejected -> expect 401
curl -s -o /dev/null -w '%{http_code}\n' https://gojoe.run/api/links
```

Logs if anything is off:

```sh
sudo journalctl -u url-shortner -n 50 --no-pager
sudo journalctl -u caddy -n 50 --no-pager
```

---

## 4. Backfill Joe's existing links (after the API is live)

`deploy/seed-links.txt` holds Joe's existing public pages as `slug url` per line.
Build the CLI once, then loop it over the file:

```sh
cd /opt/url-shortner
sudo /usr/local/go/bin/go build -o /usr/local/bin/shortcli ./cmd/shortcli

export GOJOE_BASE_URL=https://gojoe.run
export GOJOE_API_TOKEN=$(sudo sed -n 's/^APP_PASSWORD=//p' /etc/url-shortner.env)  # carries the app password

while read -r slug url; do
  [ -z "$slug" ] && continue
  case "$slug" in \#*) continue ;; esac
  shortcli create "$slug" "$url"
done < /opt/url-shortner/deploy/seed-links.txt

shortcli list   # confirm they were created
```

(The clawd `short-url` skill is the equivalent for the assistant: it reads the
token from passctl and wraps the same API.)

---

## 5. Backups (optional, one-time) — Litestream

Stream the SQLite DB to object storage for point-in-time recovery.

```sh
# Install Litestream (see https://litestream.io/install/ for the current asset).
# Configure /etc/litestream.yml to replicate the DB to an S3-compatible bucket:
cat <<'EOF' | sudo tee /etc/litestream.yml >/dev/null
dbs:
  - path: /var/lib/url-shortner/shortener.db
    replicas:
      - type: s3
        bucket: <your-bucket>
        path: url-shortner
        endpoint: <your-s3-endpoint>
        # AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY via /etc/default/litestream
EOF
sudo systemctl enable --now litestream
sudo systemctl status litestream --no-pager
```

Restore (disaster recovery): stop the service, `litestream restore` the DB file
back to `DATABASE_PATH`, then start the service.
```
```
