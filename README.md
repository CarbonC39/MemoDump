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

### Configuration sources

Settings can be supplied three ways, in priority order:

| Priority | Source | Notes |
|----------|--------|-------|
| 1 (highest) | CLI flags | `--user alice --pass secret` |
| 2 | Environment variables | `MEMODUMP_DATA`, `MEMODUMP_USER`, `MEMODUMP_PASS`, `MEMODUMP_PORT`, `MEMODUMP_CSS` |
| 3 (lowest) | `.env` file in working directory | `DATA=`, `USER=`, `PASS=`, `PORT=`, `CSS=` |

**.env file example**

```env
DATA=./notes
USER=alice
PASS=secret
PORT=9090
CSS=./custom.css
```

Lines starting with `#` and blank lines are ignored. Values are not quote-stripped.

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
