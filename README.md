# MemoDump

![MemoDump](frontend/public/icon-512.png)

A lightweight, single-binary Markdown notes app. Run it as a self-hosted web server or as a native desktop application (via [Wails](https://wails.io/)).

## Features

- **Single binary:** Go backend with embedded Vue 3 frontend — drop it anywhere and run.
- **Markdown editor:** [Milkdown](https://milkdown.dev/) WYSIWYG editor with full Markdown support.
- **Folder organisation:** Hierarchical folders with drag-and-drop and button-based `.md` file import.
- **Full-text search:** Fast in-memory search across all notes.
- **Flexible auth:** Username/password session auth, or no-auth mode for personal/trusted-network use.
- **Config layering:** Flags → environment variables → `.env` file (any combination works).
- **Desktop app:** Native window via Wails — same codebase, no browser required.
- **Mobile/PWA friendly:** Responsive design with back-navigation support.

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

### Configuration sources

Settings can be supplied three ways, in priority order:

| Priority | Source | Notes |
|----------|--------|-------|
| 1 (highest) | CLI flags | `--user alice --pass secret` |
| 2 | Environment variables | `MEMODUMP_DATA`, `MEMODUMP_USER`, `MEMODUMP_PASS`, `MEMODUMP_PORT` |
| 3 (lowest) | `.env` file in working directory | `DATA=`, `USER=`, `PASS=`, `PORT=` |

**.env file example**

```env
DATA=./notes
USER=alice
PASS=secret
PORT=9090
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

## Building

### Prerequisites

- Go 1.25+
- Node 20+ with npm

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
└── .github/workflows/build.yml
```

---

## GitHub Actions

The workflow (`.github/workflows/build.yml`) runs on every push/PR to `public` and on `v*` tags.

### `build-server` — CLI cross-compilation

Builds on `ubuntu-latest` for all targets:

| Target | Output |
|--------|--------|
| Linux amd64 | `memodump-linux-amd64` |
| Linux arm64 | `memodump-linux-arm64` |
| Linux arm | `memodump-linux-arm` |
| Windows amd64 | `memodump-windows-amd64.exe` |
| Windows 386 | `memodump-windows-386.exe` |
| macOS amd64 | `memodump-darwin-amd64` |
| macOS arm64 | `memodump-darwin-arm64` |

### `build-desktop` — Wails native builds

Runs natively on each platform (required for the native WebView dependency):

| Runner | Output |
|--------|--------|
| `windows-latest` | `memodump-desktop-windows-amd64.exe` |
| `macos-latest` | `memodump-desktop-macos-universal.zip` (contains `MemoDump.app`) |
| `ubuntu-latest` | `memodump-desktop-linux-amd64` |

### `release` — GitHub Release

Triggered only on `v*` tags. Downloads all artifacts and attaches them to a GitHub Release with auto-generated release notes.
