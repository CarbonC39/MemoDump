# MemoDump API v2 Contract

Status: implementation contract  
Version: 2026-07-29

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

## Mutations

- Create returns `201 NoteDocument`.
- Update returns `200 NoteDocument`.
- Rename/move additionally returns `previousId`.
- Name/path collision returns `409 note_name_conflict`.
- Missing notes return `404 note_not_found`.
- Invalid names return `400 invalid_note_name`.
- Request bodies larger than 10 MiB return `413 request_too_large`.

Names are trimmed, limited to 200 Unicode code points, made safe on Windows and
POSIX, and stored with one `.md` suffix. Tags preserve order and exact string
content, including commas, quotes and backslashes.

## Compatibility

Existing `/api/*` endpoints remain available during migration. New frontend
repository code targets `/api/v2`; old response shapes do not leak beyond the
legacy adapter.
