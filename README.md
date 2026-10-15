# pst

A small, self-hostable pastebin with end-to-end encryption. Pastes are
encrypted in the browser; the server only ever stores ciphertext. Ships as a
single static Go binary with the UI embedded and SQLite for storage.

## Features

- **End-to-end encrypted.** AES-256-GCM in the browser via WebCrypto. The key
  lives in the URL fragment (`/AbC123xyz0#key`), which browsers never send to
  the server.
- **Optional paste password.** The key becomes PBKDF2-SHA256(url key + password,
  600k iterations), so a leaked link alone is not enough.
- **Burn after reading.** It is deleted atomically on first open. Opening needs
  an explicit click (a POST), so Slack, iMessage and other link-preview bots
  can't burn it by fetching the URL.
- **Expiry.** Choices run from 10 minutes to 1 year, or never. A background
  sweeper removes expired pastes, and expired pastes are never served.
- **Delete tokens.** One is issued at creation and only its hash is stored. The
  creating browser remembers it and shows a Delete button.
- **Write password (feature flag).** When enabled, creating a paste needs a
  password, while reads stay open to anyone with the link. Wrong passwords are
  rate limited.
- **Hardened by default.** Strict CSP with Trusted Types, paste text rendered
  only via `textContent`, `no-referrer`, `nosniff`, no framing, per-IP rate
  limits, and request logs that never contain paste IDs.

## Run it

```bash
go run ./cmd/pst
```

Open http://localhost:8080. Configuration is via environment variables (see
[.env.example](.env.example)).

To require a password for writes:

```bash
PST_REQUIRE_WRITE_PASSWORD=true PST_WRITE_PASSWORD='a-long-random-string' go run ./cmd/pst
```

### Docker

```bash
cp .env.example .env   # set PST_WRITE_PASSWORD
docker compose up -d --build
```

### Behind a reverse proxy (e.g. `pst.deve.sh`)

pst speaks plain HTTP; terminate TLS in front of it. With Caddy:

```
pst.deve.sh {
	reverse_proxy 127.0.0.1:8080 {
		header_up X-Real-IP {remote_host}
	}
	header Strict-Transport-Security "max-age=63072000; includeSubDomains"
}
```

Then set `PST_CLIENT_IP_HEADER=X-Real-IP` so rate limits apply per client
instead of to the proxy. Behind Cloudflare, use `CF-Connecting-IP`.

**Always serve pst over HTTPS.** The write password and the page's JavaScript,
which holds the decryption key, must not travel in the clear.

## Configuration

| Variable | Default | Description |
|---|---|---|
| `PST_ADDR` | `:8080` | Listen address |
| `PST_DB_PATH` | `data/pst.db` | SQLite file |
| `PST_META_STORE` | `sqlite` | Metadata backend |
| `PST_BLOB_STORE` | `sqlite` | Content backend |
| `PST_ID_LENGTH` | `10` | Paste ID length (base62, 6–64) |
| `PST_MAX_PASTE_BYTES` | `2097152` | Max encrypted paste size |
| `PST_ALLOW_NEVER_EXPIRE` | `true` | Offer "Never" as an expiry |
| `PST_REQUIRE_WRITE_PASSWORD` | `false` | Require a password to create pastes |
| `PST_WRITE_PASSWORD` | | The write password (≥ 12 chars when required) |
| `PST_CLIENT_IP_HEADER` | | Trusted proxy header holding the client IP |

## API

All paste content is an opaque encrypted envelope produced by
[`web/static/js/crypto.js`](web/static/js/crypto.js).

| Method | Path | Notes |
|---|---|---|
| `GET` | `/api/config` | Expiry choices, size limit, whether a write password is required |
| `POST` | `/api/pastes` | Body `{content, expiresIn, burnAfterRead}`; `expiresIn` is seconds (0 = never). Needs `Authorization: Bearer <write password>` when the flag is on. Returns `id` and `deleteToken` |
| `GET` | `/api/pastes/{id}` | Returns content, or metadata only for burn-after-read pastes |
| `POST` | `/api/pastes/{id}/open` | Returns content and burns burn-after-read pastes |
| `DELETE` | `/api/pastes/{id}` | Needs `X-Delete-Token` header |
| `GET` | `/healthz` | Liveness |

## Architecture

```
cmd/pst/            entrypoint, config wiring, storage backend selection
internal/paste/     domain: Paste model, MetaStore/BlobStore interfaces, Service
internal/paste/pastetest/  conformance tests every storage backend must pass
internal/store/sqlite/     SQLite implementation of both interfaces
internal/httpapi/   HTTP handlers, security headers, rate limiting
web/static/         UI (plain HTML/CSS/JS modules, embedded into the binary)
```

Storage is split into two interfaces so each can scale independently:

- **`paste.MetaStore`** holds small rows keyed by ID. `Create` must be atomic on
  ID collision and `Take` must be an atomic get-and-delete (for burn after
  reading).
- **`paste.BlobStore`** holds paste content keyed by ID. This is the piece to
  swap for S3, R2 or the filesystem.

### Adding a backend

1. Implement `paste.MetaStore` or `paste.BlobStore` in `internal/store/<name>/`.
2. Run the conformance suite from your package's tests:
   ```go
   func TestBlobStore(t *testing.T) {
       pastetest.TestBlobStore(t, func(t *testing.T) paste.BlobStore { return newTestStore(t) })
   }
   ```
3. Add a case to `openStores` in [`cmd/pst/main.go`](cmd/pst/main.go) and select
   it with `PST_META_STORE` / `PST_BLOB_STORE`.

Backends can be mixed, for example SQLite or Postgres metadata with S3 content.
Writes reserve the ID in the metadata store before writing content, so an ID
collision can never overwrite another paste's blob.

## Threat model

The server, and anyone who compromises it, can see:
- encrypted envelopes, their sizes, and their creation and expiry times
- whether a paste is burn-after-read
- client IP addresses (in rate limiter memory; not logged)

It cannot see paste text, the decryption key, or paste passwords.

The main trust assumption is the JavaScript the server serves. A compromised
server could serve modified JS that leaks keys. The strict CSP blocks
third-party scripts, but not a malicious operator. If that matters to you,
self-host it (that's the point) or use a non-browser client.

## Development

```bash
go test ./...
node --test "web/test/*.test.mjs"
go vet ./...
```

## License

[AGPL-3.0](LICENSE). If you run a modified version as a network service, you must make its source available to its users.
