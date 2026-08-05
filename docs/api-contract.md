# MemoDump API v2 Contract

Status: implementation contract  
Version: 2026-08-04 (Phase 0: local revision + repository boundary)

## Identity

`id` is an opaque string whose current representation is a normalized,
slash-relative path. Clients must not parse or concatenate it. Rename and move
return a new `id`; mutation responses include `previousId` when identity changes.

The root folder ID is the empty string.

## Types

```ts
interface NoteSummary {
  id: string
  name: string
  parentId: string
  tags: string[]
  modifiedAt: number // Unix milliseconds
  preview: string
}

interface NoteDocument extends NoteSummary {
  content: string
  revision: string // opaque local CAS token — see Optimistic concurrency
}

interface FolderSummary {
  id: string
  name: string
  parentId: string
  hasChildren: boolean
}

interface Page<T> {
  items: T[]
  nextCursor: string | null
}

interface ApiError {
  error: {
    code: string
    message: string
  }
}
```

## Optimistic concurrency (Phase 0)

`NoteDocument.revision` is an **opaque, versioned digest** of the adapter's
durable local representation — raw Markdown bytes for filesystem notes, the
canonical note record for IndexedDB. It is local CAS state, never a remote
content hash, and is **not compared across replicas**. A same-content rewrite
may retain the same revision; a content change cannot.

- `PUT /api/v2/notes/{path}` and `DELETE /api/v2/notes/{path}` require
  `baseRevision`.
- A stale `baseRevision` returns `409 local_revision_conflict` **without
  touching the destination**; the server re-reads the current bytes under the
  per-path lock and compares before writing.
- Applying a remote sync change or an external filesystem change advances the
  revision. An editor holding stale bytes keeps its buffer and enters a local
  conflict flow instead of overwriting.
- Applies even while cloud sync is disabled: it protects self-hosted
  multi-tab / multi-client editing.

The server serializes its own concurrent requests per path (create locks the
target; update/delete lock the source; rename/move lock source and target in
sorted order). An external editor racing the final verification-and-rename
window is a platform boundary that ordinary filesystems cannot fully close.

## Listing

```http
GET /api/v2/folders?parent={folderId}
GET /api/v2/notes?parent={folderId}&limit=50&cursor={cursor}&sort=modified-desc
GET /api/v2/search?q={query}&tag={tag}&limit=50&cursor={cursor}
```

- `folders` returns direct child folders only.
- `notes` returns direct child notes only.
- `limit` defaults to 50 and is clamped to 1–200.
- note ordering is `(modifiedAt DESC, id ASC)`.
- cursors are opaque URL-safe strings containing the final ordering tuple.
- a malformed cursor returns `400 invalid_cursor`.

## Note mutations

```http
GET    /api/v2/notes/{path}            → 200 NoteDocument
POST   /api/v2/notes                   → 201 NoteDocument
PUT    /api/v2/notes/{path}            → 200 NoteDocument
DELETE /api/v2/notes/{path}            → 200 { status: "ok" }
PUT    /api/v2/move/{path}             → 200 NoteDocument
POST   /api/v2/duplicate/{path}        → 201 NoteDocument
```

- `GET` returns the document including `revision`.
- `POST` body: `{ name, folder?, content, tags? }`. A name that sanitizes to
  nothing falls back to a timestamp name.
- `PUT` body: `{ content?, tags?, rename?, destination?, baseRevision }`.
  `baseRevision` is **required**. `rename` and `destination` are applied
  together in one CAS-guarded mutation (target = `destination` / newName), so
  content, rename and move cannot be torn apart by a network failure or a
  concurrent writer.
- `DELETE` query: `?baseRevision=...` (**required**).
- `PUT /api/v2/move/{path}` body: `{ destination }` (empty = root).
- Rename/move responses include `previousId` when the `id` changed.

### Error codes

| Code | HTTP | Condition |
|---|---|---|
| `invalid_note_path` | 400 | path escapes the repository or is unusable |
| `invalid_request` | 400 | malformed JSON body |
| `base_revision_required` | 400 | `baseRevision` missing on update/delete |
| `front_matter_not_editable` | 400 | front matter cannot be safely modified |
| `note_not_found` | 404 | no such note |
| `local_revision_conflict` | 409 | `baseRevision` is stale — nothing written |
| `note_name_conflict` | 409 | destination path already exists |
| `mutation_failed` | 500 | other storage error |

## Compatibility

Existing `/api/*` endpoints remain available during migration. New frontend
repository code targets `/api/v2`; old response shapes do not leak beyond the
legacy adapter. Legacy `PUT /api/notes/{path}` and `DELETE` accept an *optional*
`baseRevision` (body field / `?baseRevision=` query) and return the same `409`
when it is stale; without it they behave as before.
