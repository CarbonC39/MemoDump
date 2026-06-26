# Note Duplicate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a "Duplicate" right-click action that copies a note (raw bytes, front matter preserved) into the same folder with a `(copy)` suffix, working across the Go server backend, the IndexedDB local backend, and the UI.

**Architecture:** A new `POST /api/duplicate/{path...}` Go handler reads the source file's raw bytes and writes them to a uniquely-named `(copy)` path in the same directory. The frontend `apiClient.duplicateNote(path)` calls it; `localApi.duplicateNote(path)` mirrors it for the no-server IndexedDB build. The context menu gains a Duplicate item whose handler duplicates then refreshes the list without changing the current view.

**Tech Stack:** Go 1.26 (net/http), Vue 3 `<script setup>`, vitest + fake-indexeddb (the only test harness in the repo).

## Global Constraints

- All commands run from the repo root `/home/cc/workspace/MemoDump` unless a step says otherwise. Frontend commands run from `frontend/`.
- **No Go test suite exists** (per CLAUDE.md). Verify Go changes with `go build .` + `go vet ./...` plus the HTTP smoke in Task 2. Do not invent `go test` commands.
- The only automated test surface is `frontend/src/localApi.test.js`, run with `npm test` (vitest) from `frontend/`.
- `frontend/dist` already exists, so `go build` works; but after any frontend change, run `npm run build` (from `frontend/`) before relying on a Go build that embeds the SPA.
- Duplicate semantics (copied verbatim from the spec): copy **raw bytes** so YAML front matter + tags survive byte-for-byte; name = `<base> (copy).md`, de-colliding as `(copy 2)`, `(copy 3)`…; always in the **same folder** as the source; 404 if the source is missing, 400 on an illegal path. After duplicating, the UI refreshes the list and **stays on the current view** (does not open the copy).
- Every task ends with a commit. Commit messages end with a blank line and `Co-Authored-By: Claude <noreply@anthropic.com>`.

---

## Task 1: `localApi.duplicateNote` (IndexedDB backend) — TDD

**Files:**
- Test: `frontend/src/localApi.test.js` (add a `describe('duplicateNote', ...)` block)
- Modify: `frontend/src/localApi.js` (add `duplicateNote` to the `localApi` object)

**Interfaces:**
- Consumes: existing `getNoteRec`, `dirname`, `noteName`, `plainTags`, `ensureFolders`, `write`, `toFull`, `apiError` (all already defined in `localApi.js`).
- Produces: `localApi.duplicateNote(path)` → `Promise<{ data: { path, name, content, tags, modTime } }>`; rejects with `{ response: { status: 404 } }` when `path` is missing.

- [ ] **Step 1: Write the failing tests**

Append to `frontend/src/localApi.test.js` (after the existing `describe('moveNote', ...)` block, before the final `describe('uploadNote'...)` if present, or at end of file):

```js
describe('duplicateNote', () => {
  it('creates a (copy) in the same folder with same content and tags', async () => {
    const src = (await localApi.createNote({ name: 'orig', content: 'body', tags: ['a', 'b'] })).data
    const dup = (await localApi.duplicateNote(src.path)).data
    expect(dup.path).toBe('orig (copy).md')
    expect(dup.content).toBe('body')
    expect(dup.tags).toEqual(['a', 'b'])
    // original is untouched
    expect((await localApi.getNote('orig.md')).data.content).toBe('body')
  })

  it('de-collides with (copy 2), (copy 3)', async () => {
    const src = (await localApi.createNote({ name: 'note', content: 'x' })).data
    const d1 = (await localApi.duplicateNote(src.path)).data
    const d2 = (await localApi.duplicateNote(src.path)).data
    expect(d1.path).toBe('note (copy).md')
    expect(d2.path).toBe('note (copy 2).md')
  })

  it('duplicates into the same subfolder', async () => {
    await localApi.createFolder('docs')
    const src = (await localApi.createNote({ name: 'infolder', folder: 'docs', content: 'hi' })).data
    const dup = (await localApi.duplicateNote(src.path)).data
    expect(dup.path).toBe('docs/infolder (copy).md')
  })

  it('404s for a missing source', async () => {
    await expect(localApi.duplicateNote('nope.md')).rejects.toMatchObject({ response: { status: 404 } })
  })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run (from `frontend/`):
```bash
npm test
```
Expected: 4 failures in `duplicateNote` — `localApi.duplicateNote is not a function` (or `undefined`).

- [ ] **Step 3: Implement `duplicateNote`**

In `frontend/src/localApi.js`, add this method to the `localApi` object. Insert it immediately after the existing `deleteNote` method (keep the trailing comma consistent with surrounding methods):

```js
  async duplicateNote(path) {
    if (!path) return apiError(400, 'Path is illegal')
    const src = await getNoteRec(path)
    if (!src) return apiError(404, 'File not found')
    const dir = dirname(path)
    const base = noteName(path)
    let filename = `${base} (copy).md`
    let i = 2
    while (await getNoteRec(dir ? dir + '/' + filename : filename)) {
      filename = `${base} (copy ${i}).md`
      i++
    }
    const newPath = dir ? dir + '/' + filename : filename
    const now = Date.now()
    const rec = { path: newPath, content: src.content || '', tags: plainTags(src.tags), modTime: now, created: now }
    await write((notes, folders) => {
      notes.put(rec)
      ensureFolders(folders, dir)
    })
    return { data: toFull(rec) }
  },
```

- [ ] **Step 4: Run tests to verify they pass**

Run (from `frontend/`):
```bash
npm test
```
Expected: all tests pass, including the 4 new `duplicateNote` tests.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/localApi.js frontend/src/localApi.test.js
git commit -m "feat(local): duplicateNote — copy a note with (copy) suffix in IndexedDB backend

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 2: Go `handleDuplicateNote` + route

**Files:**
- Modify: `api.go` (add `handleDuplicateNote`; `fmt`, `os`, `path/filepath`, `strings` are already imported)
- Modify: `server.go` (register `POST /api/duplicate/{path...}`)

**Interfaces:**
- Consumes: existing `safePath`, `dataDir`, `readNote`, `writeJSON` (all in `api.go`).
- Produces: HTTP route `POST /api/duplicate/{path...}` → `201` with the new `Note` JSON; `404` if missing, `400` if path illegal, `500` on IO failure.

- [ ] **Step 1: Add the handler**

In `api.go`, add this function immediately after `handleMoveNote` (find `func handleMoveNote` and place this after its closing brace, before `func handleListFolders`):

```go
// handleDuplicateNote creates a copy of a note in the same folder. The raw file
// bytes are copied verbatim so YAML front matter and tags survive byte-for-byte.
// The copy is named "<base> (copy).md", de-colliding as "(copy 2)", "(copy 3)"...
func handleDuplicateNote(w http.ResponseWriter, r *http.Request) {
	notePath := r.PathValue("path")
	fullPath, err := safePath(dataDir, notePath)
	if err != nil {
		http.Error(w, `{"error":"Path is illegal"}`, http.StatusBadRequest)
		return
	}
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		http.Error(w, `{"error":"File not found"}`, http.StatusNotFound)
		return
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		http.Error(w, `{"error":"Failed to read note"}`, http.StatusInternalServerError)
		return
	}

	dir := filepath.Dir(fullPath)
	base := strings.TrimSuffix(filepath.Base(fullPath), ".md")
	candidate := base + " (copy).md"
	n := 2
	for {
		destPath, err := safePath(dir, candidate)
		if err != nil {
			http.Error(w, `{"error":"Path is illegal"}`, http.StatusBadRequest)
			return
		}
		if _, err := os.Stat(destPath); os.IsNotExist(err) {
			if err := os.WriteFile(destPath, data, 0644); err != nil {
				http.Error(w, `{"error":"Failed to save note"}`, http.StatusInternalServerError)
				return
			}
			note, _ := readNote(destPath, dataDir, true)
			writeJSON(w, http.StatusCreated, note)
			return
		}
		candidate = fmt.Sprintf("%s (copy %d).md", base, n)
		n++
	}
}
```

- [ ] **Step 2: Register the route**

In `server.go`, add this line immediately after the `PUT /api/move/{path...}` line (the existing note-move route):

```go
	mux.HandleFunc("POST /api/duplicate/{path...}", authMiddleware(handleDuplicateNote))
```

- [ ] **Step 3: Verify it compiles and vets**

Run (from repo root):
```bash
go build .
go vet ./...
```
Expected: both succeed with no output (exit 0).

- [ ] **Step 4: HTTP smoke test (the only behavioral check, since there is no Go test suite)**

Run this block from the repo root. It builds the server, runs it no-auth against a temp data dir, creates a note, duplicates it, asserts the copy exists, then cleans up:

```bash
go build -o /tmp/memodump-dup-test . && \
rm -rf /tmp/md-dup-test && mkdir -p /tmp/md-dup-test && \
/tmp/memodump-dup-test --data /tmp/md-dup-test >/tmp/md-dup-test.log 2>&1 & \
SVPID=$!; \
sleep 1 && \
echo "--- create source ---" && \
curl -s -X POST localhost:8080/api/notes -H 'Content-Type: application/json' -d '{"name":"orig","content":"# Hi\nbody","tags":["x"]}' && echo && \
echo "--- duplicate ---" && \
curl -s -X POST localhost:8080/api/duplicate/orig.md && echo && \
echo "--- duplicate again (should be copy 2) ---" && \
curl -s -X POST localhost:8080/api/duplicate/orig.md && echo && \
echo "--- files on disk ---" && \
ls /tmp/md-dup-test && \
kill $SVPID 2>/dev/null; rm -f /tmp/memodump-dup-test; rm -rf /tmp/md-dup-test
```
Expected: the duplicate responses return `201` JSON whose `path` is `orig (copy).md` then `orig (copy 2).md`; `ls` shows `orig.md`, `orig (copy).md`, `orig (copy 2).md`. Front matter/tags are preserved because bytes were copied.

- [ ] **Step 5: Commit**

```bash
git add api.go server.go
git commit -m "feat(api): POST /api/duplicate — copy a note with (copy) suffix

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 3: `remoteApi.duplicateNote` (axios client)

**Files:**
- Modify: `frontend/src/api.js` (add `duplicateNote` to the `remoteApi` object)

**Interfaces:**
- Consumes: the `api` axios instance and the `/duplicate` route from Task 2.
- Produces: `apiClient.duplicateNote(path)` (active when `VITE_LOCAL !== '1'`) → `Promise<{ data: Note }>`.

- [ ] **Step 1: Add the method**

In `frontend/src/api.js`, add this method to the `remoteApi` object, immediately after the existing `moveNote` method (keep the trailing comma consistent):

```js
    duplicateNote(path) {
        return api.post(`/duplicate/${path}`)
    },
```

- [ ] **Step 2: Verify the frontend builds**

Run (from `frontend/`):
```bash
npm run build
```
Expected: build succeeds (Vite production build, exit 0).

- [ ] **Step 3: Commit**

```bash
git add frontend/src/api.js
git commit -m "feat(api.js): duplicateNote client for the Go duplicate endpoint

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Task 4: Context-menu "Duplicate" item + handler in `MainView.vue`

**Files:**
- Modify: `frontend/src/views/MainView.vue` — add the menu item in the context-menu template block and add `menuDuplicateNote()` in the script. (The menu is still inline in `MainView.vue` here; a later split plan moves it into `ContextMenu.vue` and will carry this item along.)

**Interfaces:**
- Consumes: `apiClient.duplicateNote` (Tasks 1 & 3), the existing `contextMenu` reactive state, `closeContextMenu`, and `loadAll`.
- Produces: a `menuDuplicateNote` handler bound from the template.

- [ ] **Step 1: Add the menu item to the template**

In `frontend/src/views/MainView.vue`, find the context-menu block (the `<div v-if="contextMenu.visible" class="context-menu" ...>` containing the Edit / Copy Full Text / Download / Move / Delete items). Insert this item immediately **after** the "Copy Full Text" item (`@click="menuCopyContent"`) and before the "Download" item:

```html
      <div class="context-menu-item" @click="menuDuplicateNote">
        <span class="material-icons-outlined">file_copy</span> Duplicate
      </div>
```

- [ ] **Step 2: Add the handler in the script**

In the same file's `<script setup>`, add this function alongside the other `menu*` handlers (e.g., immediately after `menuCopyContent`):

```js
async function menuDuplicateNote() {
  const note = contextMenu.note
  closeContextMenu()
  if (!note) return
  try {
    await apiClient.duplicateNote(note.path)
    await loadAll()
  } catch (e) { alert('Duplicate failed') }
}
```

- [ ] **Step 3: Verify the frontend builds**

Run (from `frontend/`):
```bash
npm run build
```
Expected: build succeeds.

- [ ] **Step 4: Manual smoke**

Build and run the app, then exercise the new action:
```bash
# from repo root
go build -o /tmp/memodump-smoke . && \
rm -rf /tmp/md-smoke && mkdir -p /tmp/md-smoke && \
/tmp/memodump-smoke --data /tmp/md-smoke >/tmp/md-smoke.log 2>&1 & \
SVPID=$!; sleep 1; \
echo "open http://localhost:8080 — create a note, right-click it, choose Duplicate"; \
echo "expect: a '<name> (copy)' note appears in the same folder, current view unchanged"; \
echo "press Enter here when done checking"; read _; \
kill $SVPID 2>/dev/null; rm -f /tmp/memodump-smoke; rm -rf /tmp/md-smoke
```
Expected: the duplicate appears with a `(copy)` suffix in the same folder; the currently open note / selection is unchanged; no error alert.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/views/MainView.vue
git commit -m "feat(ui): add Duplicate to the note context menu

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

## Definition of Done

- `npm test` passes (including the 4 new `duplicateNote` tests).
- `go build .` and `go vet ./...` pass; the HTTP smoke shows `(copy)` / `(copy 2)` files on disk with front matter preserved.
- `npm run build` passes.
- Right-click → Duplicate creates a same-folder `(copy)` and leaves the current view unchanged, in both the server build and the `VITE_LOCAL=1` IndexedDB build.
