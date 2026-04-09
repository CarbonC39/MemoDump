# MemoDump

![MemoDump](frontend/public/icon-512.png)

A lightweight, high-performance web-based markdown memo application designed for instant startup, effortless viewing, and organized archiving. MemoDump provides a clean interface for managing your personal notes with a focus on simplicity and speed.

## Features

- **Instant Startup:** Written in Go with an embedded frontend for a single-binary experience.
- **Markdown-first:** Uses [Milkdown](https://milkdown.dev/) for a modern, extensible WYSIWYG markdown editing experience.
- **Folder Organization:** Hierarchical folder structure for organizing notes.
- **Front Matter Support:** YAML front matter for tags and metadata.
- **Full-text Search:** Fast search across all notes with a memory-cached index.
- **Mobile Friendly:** Responsive design that works great on desktops and mobile devices.
- **Secure:** Simple username/password authentication with session management.

## Tech Stack

### Backend
- **Language:** Go 1.22+
- **Server:** Standard library `net/http` with `http.ServeMux`
- **Security:** Session-based authentication, path sanitization
- **Caching:** `sync.Map` based note cache for fast searching and listing

### Frontend
- **Framework:** Vue.js 3
- **Editor:** Milkdown (Markdown WYSIWYG)
- **Router:** Vue Router
- **Build Tool:** Vite
- **Styling:** Vanilla CSS

## Installation & Building

### Prerequisites
- [Go](https://golang.org/dl/) (1.22 or later)
- [Node.js](https://nodejs.org/) (for building the frontend)

### Build Steps

1. **Build the Frontend:**
   ```bash
   cd frontend
   npm install
   npm run build
   cd ..
   ```

2. **Build the Backend:**
   ```bash
   go build -o memodump
   ```

## Usage

Run the compiled binary with the required flags:

```bash
./memodump --data ./my-notes --user admin --pass secret123 --port 8080
```

### Flags
- `--data`: Path to the directory where markdown files will be stored.
- `--user`: Username for logging into the web interface.
- `--pass`: Password for logging into the web interface.
- `--port`: (Optional) Port to run the service on (default: 8080).

## Project Structure

- `main.go`: Application entry point and route definitions.
- `api.go`: Backend API handlers for note and folder management.
- `auth.go`: Authentication and session management logic.
- `frontend/`: Vue.js frontend application.
  - `src/components/MilkdownEditor.vue`: The markdown editor component.
  - `src/views/MainView.vue`: The primary workspace interface.
- `testdata/`: Sample data for testing purposes.
