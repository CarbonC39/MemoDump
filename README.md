# MemoDump

<p align="center">
  English | <a href="README.zh-CN.md">简体中文</a>
</p>

<p align="center">
  <img src="frontend/public/memodump.svg" alt="MemoDump logo" width=150/>
</p>

<p align="center">
  <img alt="Go" src="https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go&logoColor=white" />
  <img alt="License" src="https://img.shields.io/badge/License-MIT-green" />
  <img alt="PWA" src="https://img.shields.io/badge/PWA-ready-5A0FC8?logo=pwa&logoColor=white" />
  <img alt="Wails" src="https://img.shields.io/badge/Desktop-Wails-red?logo=go" />
  <img alt="Docker" src="https://img.shields.io/badge/Docker-ghcr.io-2496ED?logo=docker&logoColor=white" />
</p>

<p align="center">
  <a href="https://memodump.carbonc.cc/">Website</a> ·
  <a href="https://memodump.vercel.app/">Live Demo</a>
</p>

A lightweight, single-binary Markdown notes app. Run it as a self-hosted web server, a native desktop application (via [Wails](https://wails.io/)), or a Docker container.

> The [live demo](https://memodump.vercel.app/) runs in no-auth mode against ephemeral storage for trying out the editor — don't store anything you care about there.

## Features

- **Single binary** — Go backend with embedded Vue 3 frontend. Drop it anywhere and run.
- **Markdown editor** — [Milkdown](https://milkdown.dev/) WYSIWYG editor with full Markdown support.
- **Folder organisation** — Hierarchical folders with drag-and-drop and `.md` file import.
- **Full-text search** — Fast in-memory AND-mode search across note bodies and tags.
- **Image paste & upload** — Paste or drop image files to insert them; stored in the local vault by default, with optional S3-compatible hosting.
- **Waterfall card view** — Visual masonry-style note browser alongside the folder tree.
- **Autosave & offline outbox** — Calm autosave backed by IndexedDB; edits queued while offline replay automatically when connectivity returns.
- **Font presets & typography** — Choose from system, serif, and sans font families; custom CSS font stacks; independent font sizes for app UI, WYSIWYG editor, and raw editor.
- **Settings panel** — Full-page settings view with live preview card, numeric inputs, and typography controls.
- **Custom CSS** — Inject a stylesheet via `--css` CLI flag, or edit custom CSS directly in the settings panel.
- **Flexible auth** — Username/password session auth, or no-auth mode for personal/trusted-network use.
- **Config layering** — Flags → environment variables → `.env` file (any combination works).
- **Desktop app** — Native window via Wails — same codebase, no browser required.
- **Mobile/PWA friendly** — Responsive design, installable as a PWA with back-navigation support.

<p align="center">
  <img src="images/md-editor.avif" alt="Markdown editor view"/>
  <img src="images/waterfall-view.avif" alt="Waterfall notes view"/>
</p>

---

## Image support

Image storage across the three builds:

| Build | Default | Configurable |
|-------|---------|--------------|
| Web server | Local vault (`<dataDir>/.images/`) | S3-compatible (settings panel or environment) |
| Wails desktop | Local vault (`<dataDir>/.images/`) | S3-compatible (settings panel) |
| Pure frontend / PWA | Off (image links only) | S3-compatible (settings panel, browser-direct) |

- Paste or drop image files in the editor to insert them. The local vault stores
  images under the data dir and the markdown keeps a relative URL
  (`/api/images/<key>`) that resolves only inside the app origin — the
  portability tradeoff of self-hosted images.
- **S3 mode requires the bucket to be publicly readable** (otherwise images
  show 403). The pure-frontend build additionally needs bucket CORS configured
  (allow the app origin, `PUT/POST/GET/HEAD`, `Content-Type` and `x-amz-*`
  headers, and expose `ETag` for multipart uploads).
- **Privacy notice**: in S3 mode images are publicly readable by anyone with
  the link; the content hash is not access control, and identical files
  produce identical links. Do not upload images that must stay private.
- Images pasted offline are kept in browser IndexedDB and upload automatically
  once connectivity returns; an entry is removed only after the image is
  uploaded and verified readable (a few orphan objects may remain, which is
  accepted).
- **Optional periodic cleanup** (settings → Images): when enabled, the server
  periodically deletes images no note references (local vault and S3, with a
  7-day grace period; in S3 mode deletion is remote and permanent), and the
  web/Wails build additionally removes permanently-failed pending entries after
  30 days. Use a dedicated bucket/prefix for MemoDump so other files are never
  affected. Cleanup is off by default.
- Security: image keys are `sha256(content) + canonical extension` (JPEG is
  always `.jpg`); the server validates the content hash, magic bytes and the
  extension match. Only png/jpg/gif/webp/avif are accepted — **no SVG**
  (same-origin stored-XSS risk).

The web server can also be configured via environment:
`MEMODUMP_IMAGE_S3_ENDPOINT`, `_REGION`, `_BUCKET`, `_PREFIX`, `_PUBLIC_URL`,
`_ACCESS_KEY`, `_SECRET_KEY`, `_FORCE_PATH_STYLE` (higher priority than the
settings panel, which becomes read-only).

---

## CLI Server

### Quick start

```sh
# No authentication (personal / trusted-network use)
memodump --data ./notes

# With credentials
memodump --data ./notes --user alice --pass secret

# Custom port
memodump --data ./notes --user alice --pass secret --port 9090
```

Open `http://localhost:8080` in your browser (or the custom port).

### Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--data` | Path to notes directory (required) | — |
| `--user` | Login username | — |
| `--pass` | Login password | — |
| `--port` | HTTP port | `8080` |
| `--css` | Custom CSS file injected into the UI | — |
| `--sync-root` | Cloud-sync device-state root (identity, replicas, snapshot) | OS application data dir |

### Configuration sources

Settings can be supplied three ways, in priority order:

| Priority | Source | Notes |
|----------|--------|-------|
| 1 (highest) | CLI flags | `--user alice --pass secret` |
| 2 | Environment variables | `MEMODUMP_DATA`, `MEMODUMP_USER`, `MEMODUMP_PASS`, `MEMODUMP_PORT`, `MEMODUMP_CSS`, `MEMODUMP_SYNC_ROOT` |
| 3 (lowest) | `.env` file in working directory | `DATA=`, `USER=`, `PASS=`, `PORT=`, `CSS=`, `SYNC_ROOT=` |

**.env file example**

```env
DATA=./notes
USER=alice
PASS=secret
PORT=9090
CSS=./custom.css
SYNC_ROOT=./sync-state
```

### Cloud-sync state root (`--sync-root`)

Cloud sync is experimental and off by default; nothing is created until a vault
first enables it. When it is enabled, one installation keeps its sync device
state — Device ID, the path→Replica registry, and each replica's connection
record, disposable snapshot, and recovery copies — under a **state root**:

- **Default**: the OS application-data directory (`os.UserConfigDir()/memodump/sync`).
- **Override**: `--sync-root <dir>`, `MEMODUMP_SYNC_ROOT`, or `SYNC_ROOT=`.

This root lives **outside the vault**: it is never synced, never uploaded, and
must be persisted alongside the data directory. In a container, mount a volume
for it (the image sets `MEMODUMP_SYNC_ROOT=/var/lib/memodump/sync`) so replica
identity and device state survive container recreation.

Lines starting with `#` and blank lines are ignored. Values are not quote-stripped.

### Cloud sync (experimental)

Cloud sync keeps two or more MemoDump installations' Markdown notes in sync
through an S3-compatible bucket. It is **experimental and off by default**; the
settings panel shows a plaintext/no-E2EE warning. It is eventual, not
real-time, synchronization: a change on device A is uploaded on A's next run
and downloaded on B's next run, so normal latency is about two intervals.

**Provider configuration.** Set these environment variables on each installation
(the CLI flags mirror them only for the state root; the provider uses env vars):

| Variable | Meaning |
|----------|---------|
| `MEMODUMP_SYNC_ENDPOINT` | S3-compatible endpoint URL (e.g. `https://s3.region.amazonaws.com`). Plain HTTP is allowed only for `localhost`/loopback development. The endpoint must not carry a path. |
| `MEMODUMP_SYNC_BUCKET` | A **private** bucket (never public-read). |
| `MEMODUMP_SYNC_PREFIX` | Optional object prefix (e.g. `memo/vault-a`). |
| `MEMODUMP_SYNC_REGION` | Region (default `us-east-1`). |
| `MEMODUMP_SYNC_ACCESS_KEY` / `MEMODUMP_SYNC_SECRET_KEY` | Credentials. They are never stored in the vault or sent to the frontend; the secret-free provider profile is a hash of endpoint/bucket/prefix only. |
| `MEMODUMP_SYNC_FORCE_PATH_STYLE` | `1` for path-style addressing (MinIO, R2, LocalStack). |

All note content is synced **unencrypted** (no E2EE). Use a private bucket and
restrict its credentials. Prefer HTTPS; plain HTTP is refused except for
loopback development.

**Behavior.** A connected replica (you clicked **Enable** once) runs automatically
while MemoDump is open: once after a **10-second startup delay**, then every
**five minutes**, plus immediately after a successful Enable. `Run now` in the
settings panel still forces an immediate run. No sync runs while the process is
closed (a browser tab alone does not keep the CLI server alive — the server
process must be running). A transient provider failure retries with in-memory
backoff (`1m, 2m, 5m, 10m, 30m`, reset by success; restart forgets it). An
auth/permission/quota/mismatch failure **pauses automatic sync** for the rest of
the process — the status shows the paused reason — while `Run now` still works;
a successful manual run or Enable clears the pause.

**Cloud sync is not a backup.** Deletes propagate to every device. Provider-side
versioning/history is external to MemoDump. The durable **recovery copies** the
app writes before a pulled deletion are local safety aids: inspect and restore
them from the settings panel.

**State root contents.** The `--sync-root` state directory holds, per replica:
Device identity and the path→Replica registry, the connection record (provider
pin + repository ID), one disposable snapshot, and the recovery copies. It
contains **no WAL, cursor, or durable scheduler queue**, stays **outside the
vault**, and must persist across container/app recreation (mount a volume).

**Do not combine with another filesystem sync tool.** Do not place the same
vault under Dropbox/iCloud/OneDrive, git automation, or another file-sync tool
while MemoDump cloud sync is enabled — the two tools would race on the same
Markdown files.

**Managing the connection.** The settings panel shows connection state, the last
(redacted) run, the next scheduled run, and recovery copies. **Disable** stops
automatic runs and keeps your identity, so re-enabling with the same provider
reconnects cleanly. **Reset & reconnect** (with confirmation) discards this
replica's snapshot and connection pin so you can deliberately switch providers
or recreate a lost repository. A normal run never re-creates a lost repository
on its own.

**Testing a provider.** The opt-in live S3 test uses a random isolated prefix and
cleans up after itself; it never prints credentials:

```sh
MEMODUMP_S3_LIVE_ENDPOINT=https://… \
MEMODUMP_S3_LIVE_BUCKET=… \
MEMODUMP_S3_LIVE_ACCESS=… \
MEMODUMP_S3_LIVE_SECRET=… \
go test ./internal/syncprovider/s3/ -run TestS3Live -v
```

### No-auth mode

If no credentials are configured from any source, the server starts in **no-auth mode** — all API endpoints are accessible without a session cookie.

```sh
memodump --data ./notes
# WARNING: No credentials configured — running in no-auth mode (all requests allowed)
```

---

## Desktop App (Wails)

The Wails build wraps the same backend in a native window — no browser or open port needed.

- On first launch the data directory is resolved automatically (no dialog):
  1. `DATA=` key in a `.env` file next to the binary / in the working directory
  2. `./data` subfolder of the working directory (auto-created if absent)
- The chosen path is saved to the OS user-config directory and reused on subsequent launches.
- Use the **Data Folder** button in the sidebar to pick a different folder (restart required to apply).
- Always runs in no-auth mode (credentials are not applicable).

**Config file location**

| OS | Path |
|----|------|
| Windows | `%APPDATA%\memodump\config.json` |
| macOS | `~/Library/Application Support/memodump/config.json` |
| Linux | `~/.config/memodump/config.json` |

---

## Docker

Pre-built images are published to GitHub Container Registry on every tagged release: `ghcr.io/carbonc39/memodump`. The image runs the headless CLI server only (the Wails desktop build doesn't apply in a container).

```sh
# No authentication
docker run -d -p 8080:8080 -v ./notes:/data ghcr.io/carbonc39/memodump:latest

# With credentials and a custom port
docker run -d -p 9090:9090 -v ./notes:/data \
  -e MEMODUMP_USER=alice -e MEMODUMP_PASS=secret -e MEMODUMP_PORT=9090 \
  ghcr.io/carbonc39/memodump:latest
```

The data volume mounts to `/data` (set via `MEMODUMP_DATA=/data` inside the image). Available tags: `latest`, `vX.Y.Z`, `vX.Y`. All [CLI environment variables](#configuration-sources) work the same way inside the container.

Build the image locally with `docker build -t memodump .` (see `Dockerfile`).

---

## Building

### Prerequisites

- Go 1.25+
- Node 20+ with npm

Before preparing a release, complete the [manual testing checklist](docs/manual-testing.md).

### CLI server

```sh
# Build frontend
cd frontend && npm install && npm run build && cd ..

# Build for current platform
go build -o memodump .

# Cross-compile example (Linux arm64)
GOOS=linux GOARCH=arm64 go build -o memodump-linux-arm64 .
```

### Desktop app (Wails)

```sh
# Install Wails CLI (once)
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# Production build
wails build

# Development (hot-reload)
wails dev
```

Output is placed in `build/bin/`.

> **Note:** `wails dev` opens a terminal window for its hot-reload proxy — this is expected. Production builds (`wails build`) use `-H windowsgui` on Windows and produce a GUI-only binary with no console.

### Build tags reference

| Tag | Used by | Entry point |
|-----|---------|-------------|
| *(none)* | `go build .` | `main_cli.go` |
| `production` | `wails build` | `main_wails.go` |
| `dev` | `wails dev` | `main_wails.go` |
| `bindings` | Wails JS-binding generation (internal) | `main_wails.go` |

---

## Project structure

```
memodump/
├── main_cli.go       # CLI entry point (build tag: !production && !dev && !bindings)
├── main_wails.go     # Wails entry point (build tag: production || dev || bindings)
├── app_wails.go      # Wails App struct — startup, data-dir config, change-folder dialog
├── server.go         # Shared: package vars + buildAPIMux()
├── api.go            # Note & folder API handlers
├── auth.go           # Session auth middleware and login/logout handlers
├── wails.json        # Wails project config
├── frontend/         # Vue 3 + Milkdown frontend (built into binary via embed)
│   └── src/
│       ├── views/MainView.vue
│       └── style.css
├── Dockerfile        # Headless CLI server image (multi-stage: frontend → go build → distroless)
└── .github/workflows/build.yml   # CI/CD (primary) — see below
```

---

## CI / CD

CI runs on **GitHub Actions** (`.github/workflows/build.yml`), triggered on every push/PR to `public` and on `v*` tags.

### `build-cli` — CLI cross-compilation

Runs on every push/PR (cheap, Linux-hosted cross-compilation):

| Target | Output |
|--------|--------|
| Linux amd64 | `memodump-server-linux-amd64` |
| Linux arm64 | `memodump-server-linux-arm64` |
| Linux arm | `memodump-server-linux-arm` |
| Windows amd64 | `memodump-server-windows-amd64.exe` |
| Windows 386 | `memodump-server-windows-386.exe` |
| macOS amd64 | `memodump-server-darwin-amd64` |
| macOS arm64 | `memodump-server-darwin-arm64` |

### `build-desktop` — Wails desktop builds

Gated to `v*` tags / manual dispatch (native macOS/Windows/Linux runners bill more, so they don't run on every push): Windows amd64, macOS universal, Linux amd64.

### `docker` — Docker image

Also gated to `v*` tags / manual dispatch. Builds `linux/amd64` + `linux/arm64` and pushes to `ghcr.io/carbonc39/memodump`, tagged `latest`, `vX.Y.Z`, and `vX.Y`.

### `release` — GitHub Release

Triggered only on `v*` tags. Collects every build artifact onto a GitHub Release.

---

<p align="center">
  If you find MemoDump useful, consider <a href="https://ko-fi.com/carbonc">buying me a hot chocolate ☕</a>
</p>
