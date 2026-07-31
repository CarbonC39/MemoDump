# MemoDump V2 Plan

Status: design record — agreed during planning, not yet implemented
Date: 2026-07-31

This document records the agreed design for the next major version. Feature 1
(image support) is fully specified here; features 2 and 3 (find & replace, cloud
sync) will be appended as they are discussed.

## Feature 1 — Better image support

### Scope decisions

- **Local vault is the default image target** for the web server and Wails
  builds: images live under `<dataDir>/.images/` (dot-prefixed so the existing
  folder-tree APIs hide it) and are served via `GET /api/images/{key}`
  (same-origin relative URL in the markdown).
- **S3-compatible is the only external host in v1.** One adapter
  (endpoint / region / bucket / prefix / publicBaseUrl / accessKey / secretKey)
  covers AWS S3, Cloudflare R2, Backblaze B2, MinIO, Aliyun OSS, Tencent COS and
  Qiniu Kodo (all expose S3-compatible APIs).
- **No client-side image compression in v1.**
- **S3 buckets must be publicly readable in v1.** The markdown URL is an
  anonymous GET; a private bucket that accepts PUT but denies GET would render
  every image as 403. Private-bucket support (signed URLs / server read proxy)
  is future work.
- **S3 mode is public by design — privacy is a first-class warning.** The
  settings panel and README both state: images uploaded in S3 mode are
  publicly readable by anyone with the link; the content-hash key is *not*
  access control; identical files produce identical URLs across MemoDump
  instances. Users should not upload private images in S3 mode.

### Architecture: URL-first (no markdown rewriting)

The final image URL is known at paste time, so saved markdown never contains a
placeholder and **nothing is ever rewritten**. This eliminates the entire class
of "stale snapshot / content regression" bugs around URL rewriting.

1. On paste/drop, the client validates the file, **detects the actual image
   format from magic bytes** (not the filename), computes
   `key = sha256(blob) + canonicalExt`, and **durably persists the blob in
   IndexedDB before the editor node is inserted**.
2. The editor's `onUpload` hook returns the final URL only after that durable
   persist (and an object URL for display):
   - vault: `/api/images/<key>`
   - S3: `publicBaseUrl + "/" + prefix + key` (normalized, no duplicated
     slashes)
3. The image node carries the final URL from the start. An immediate upload
   attempt runs right after insertion; failures stay in the durable queue.
4. Offline display: Crepe's `proxyDomURL` maps pending URLs to object URLs
   (hydrated from IndexedDB on startup), so images render locally before/during
   upload.
5. Completion requires the final URL to be readable — see the lifecycle state
   machine below. The durable blob is deleted only at `completed`.
6. Permanent failures keep the blob and surface a global "待上传" status with a
   retry/configuration action. No modal dialogs.

Content-hash keys make deduplication free (the same image pasted twice reuses
one object, because the canonical extension is derived from content) and are
independent of note path, so renames/moves never break image references.

### Lifecycle state machine

Every staged image is one of:

```text
pending ──upload 2xx──▶ uploaded ──read verified──▶ completed (entry+blob removed)
   │                        │
   └── retry (backoff) ─────┴── permanent failure (blob retained, no auto retry)
```

- `pending`: blob durable in IndexedDB, upload attempts with backoff.
- `uploaded`: upload 2xx but readability not yet confirmed; **blob is still
  retained**.
- `completed`: the final URL has been verified readable; only then is the
  IndexedDB entry and blob removed.

Read verification rules:

- **Vault (web/wails)**: `PUT` 2xx is trusted as completion (documented
  boundary — same request pipeline, same origin; a later `HEAD` is optional).
- **S3 via Go proxy (web/wails)**: the proxy performs the PUT **and** a
  server-side read check (HEAD or first-bytes GET of the public URL); the
  client is told success only when both pass. Partial failure (PUT ok, read
  check fails) returns an error with kind `verify-failed` and the client stays
  `pending`/`uploaded`.
- **S3 direct (pure frontend)**: after PUT 2xx → `uploaded`. Verification is an
  anonymous GET of the final URL (status 200; content-length match when
  available), or — for an image currently rendered — the successful `load`
  event of the final URL after the display swaps off the object URL. CDN
  propagation delays make verify failures retryable; persistent 403/404 makes
  them permanent with a configuration action.

This closes the "upload succeeded, blob deleted, URL not actually readable"
window (e.g. wrong publicBaseUrl, CDN lag, bucket policy change).

### Canonical media staging flow

This is the single state machine for staging a pasted/dropped image in every
build. It covers *media staging only* — node insertion is owned separately (see
editor integration):

1. Validate type and size (whitelist, ≤ 20 MiB).
2. Detect the actual format from the first bytes; derive the canonical
   extension (JPEG → `.jpg`; PNG → `.png`; GIF → `.gif`; WebP → `.webp`;
   AVIF → `.avif`). **The original filename extension and `File.type` are not
   trusted for the key.**
3. Compute SHA-256 and the final URL (normalized).
4. **Persist the blob into IndexedDB before the editor node is inserted.**
5. Create/reuse an object URL.
6. Return the final URL to the caller.
7. Immediately attempt the upload.
8. Upload 2xx → vault: `completed`; S3: `uploaded` → verify read → `completed`.
9. Retryable failure: retain blob; update `attempts` / `nextAttemptAt` /
   `lastError` (exponential backoff, `30s → 2m → 5m → 15m → 30m` cap).
10. Permanent failure: retain blob; stop automatic retries; surface a
    retry/configuration action.
11. **Never delete a pending/uploaded entry merely because a note is deleted.**
    Orphan objects are accepted.

### Media outbox

- IndexedDB store `memodump-media`, primary key
  `id = targetId + ':' + key`, where `targetId` is a stable identifier of the
  paste-time destination (`local`, or normalized
  `s3:<endpoint>|<bucket>|<prefix>`). Records:
  `{ id, url, key, target, state, blob, attempts, nextAttemptAt, lastError,
  createdAt, uploadedAt? }`.
- `target` is an **immutable snapshot** of the destination at paste time:
  `{ provider: 'local' }` or
  `{ provider: 's3', endpoint, bucket, prefix, publicBaseUrl, forcePathStyle }`.
  **Secrets are never copied into IndexedDB records** — credentials are read
  from current settings at retry time.
- **Provider/config switch semantics**: pending entries keep their original
  destination. Retries use the currently configured credentials for that
  destination; if the destination's configuration is gone (e.g. server no
  longer S3-configured, or S3 settings changed), the entry moves to a
  `config-required` state: no automatic retries, only a configuration action.
  This is the v1 rule; migration of pending entries to a new bucket is future
  work.
- Dedupe: same `targetId + key` → reuse the existing entry/blob; a new node
  still gets inserted.
- Flush triggered by the same 30s ping timer and `online` event as the note
  outbox, but as a **separate queue** (different lifecycle). **No ordering
  dependency with the note outbox**: URLs are final at insert time, so a stale
  note snapshot can never carry a placeholder back.
- Retries are gated by `nextAttemptAt`; manual retry sets it to now.

### Error taxonomy

All upload/verify errors are normalized to one shape so the UI can distinguish
CORS, credentials, private bucket, file problems, and server problems:

```js
{ kind, retryable, message }
// kind: "network" | "auth" | "permission" | "cors" | "invalid-file"
//     | "invalid-config" | "too-large" | "verify-failed" | "server"
```

Classification:

| Situation | kind | retryable |
|---|---|---|
| offline / DNS / connection reset | network | true |
| 401 | auth | false |
| 403 | permission | false |
| 400 (hash/magic mismatch, bad key) | invalid-file | false |
| 404 on upload endpoint | invalid-config | false |
| 413 | too-large | false |
| 408 / 425 / 5xx | server | true |
| 429 | server | true (honor `Retry-After` when present) |
| 409 (conditional-write conflict) | invalid-config | false |
| S3 read check after successful PUT | verify-failed | true (CDN lag) → false after N attempts (bucket policy) |
| Browser CORS preflight failure (TypeError while online) | cors | false |
| Destination config no longer available | invalid-config | false |

CORS vs. network heuristic: when `navigator.onLine` is true and the fetch fails
with a `TypeError` before any response, classify as `cors` and stop automatic
retries; when offline, classify as `network`.

### Security requirements (server-side vault API)

- Key must match `^[a-f0-9]{64}\.(png|jpg|gif|webp|avif)$` — anchored, no
  slashes, no `.jpeg` (canonical extension is `.jpg` only), no path traversal.
- **Content↔key binding**: the server streams the body through SHA-256 and
  rejects with 400 on mismatch. Without this, any authenticated user could read
  an image key from a note's markdown and overwrite that image with arbitrary
  content (arbitrary overwrite within the images namespace).
- **`detectFormat(header)` is the single source of truth**: the canonical
  extension derived from the detected format must equal the requested key's
  extension, and `Content-Type` comes from the detected format — never from the
  URL extension or user input. Responses carry
  `X-Content-Type-Options: nosniff`.
- **SVG is excluded** from the whitelist (stored-XSS risk when served
  same-origin).
- `GET /api/images/{key}` stays behind `authMiddleware` and serves
  `Cache-Control: private, max-age=31536000, immutable` — `private`, not
  `public`, so shared caches never store authenticated responses. Same-origin
  `<img>` requests carry the session cookie; Wails runs no-auth.
- Follow the repo conventions from CLAUDE.md: `safePath` for every
  user-derived path, atomic write (tmp + rename) for image writes.
- 20 MiB limit enforced server-side and mirrored client-side. Auth + size
  limits bound junk-upload DoS for v1; quotas/rate limits are future work.

### Pure frontend build (VITE_LOCAL)

- Browser-direct S3: compute SHA-256 before PUT (same key binding); optional
  S3 `If-None-Match` conditional write.
- Access keys are stored in localStorage (accepted tradeoff; the settings UI
  must state this explicitly).
- **Bucket must allow anonymous reads** and CORS for GET/HEAD; the test
  connection verifies PUT, anonymous GET, and CORS preflight, so a private
  bucket fails with a clear message instead of 403s later.
- Bucket CORS is required: allow the app origin, PUT/POST, `Content-Type` and
  `x-amz-*` headers, GET/HEAD, and expose `ETag` for multipart uploads.

### Settings matrix

| Build | Where the S3/vault config lives |
|-------|---------------------------------|
| web server | Server-level config, UI-editable: flags → env → `.env` → `<dataDir>/.image-config.json` (lowest priority); the panel edits the data-dir file |
| Wails | OS user-config dir (`memodump/image-config.json`, next to the existing `config.json`), UI-editable (desktop app: the user owns the machine) |
| Pure frontend | Browser localStorage / settings panel |

Web/wails both edit the same server-side config through the settings panel; the
UI is read-only only when the effective config comes from a higher-priority
source (flags/env/`.env`). Secrets transit browser → server once over the
authenticated API and are never returned to the client; `GET /api/config`
exposes only provider / bucket / publicBaseUrl / prefix / configured / editable.
Allowing any logged-in user to change this is consistent with MemoDump's
single-account trust model (they can already read and delete every note).

Wails keeps the image config **out of the data directory** on purpose: data
dirs are often placed inside cloud-sync folders (OneDrive, etc.), and
long-lived S3 credentials should not ride along with the synced notes.

## Deferred / known issues

- **Crepe block images serialize alt as ratio**: `image-block` stores
  `src/caption/ratio` and `toMarkdown` emits `![ratio](src "caption")`, so user
  alt text is lost (accessibility). Fix later at the component/schema level.
- Private S3 buckets (signed URLs / server read proxy).
- Migrating pending entries when the S3 config changes.
- Shared S3 buckets across users: document that each user should use their own
  token/bucket.
- Upload quota / rate limiting.

## Feature 1 task list

**A. Editor**
1. Paste/drop interception + `image-block` node insertion (canonical media
   staging flow, manual node insert; no double-insert path).
2. Wire `onUpload` / `proxyDomURL` / `onImageLoadError` (Crepe owns node
   insertion for the upload button).

**B. Storage backends**
3. Go vault API: `PUT /api/images/{key}` (SHA-256 binding, `detectFormat`
   single source of truth, canonical extensions only, existing-key repair,
   atomic write) + `GET /api/images/{key}` (`nosniff`, `private` immutable
   cache).
4. Go S3 proxy adapter (web/wails): required-field validation, URL
   normalization + validation, config persistence + precedence + secret
   rotation rules, server-side test connection, PUT + read-verify.
5. Pure-frontend S3 direct PUT via a mature tree-shakeable signing library
   (spike `aws4fetch` vs `@smithy/signature-v4`), hash-bind, read-verify,
   test connection with anonymous-read check.

**C. Queue**
6. Media outbox (IndexedDB): durable enqueue-first, immediate attempt,
   lifecycle states, backoff, startup hydration, object-URL lifecycle,
   config-required handling.

**D. Settings**
7. Image section: mode (vault / S3 / off), config form, test connection, CORS
   template, public-read + privacy warnings, localStorage-key warning for the
   pure-frontend build.

**E. Docs**
8. README updates: per-build support matrix, CORS configuration guide, public
   read + privacy requirements, security notes (key binding, bucket
   permissions, orphan objects, hash-URL semantics).

---

# Implementation Plan — Feature 1 (for review)

Status: awaiting review
Branch: `feat/v2-image-support` (based on `public`)

## Workflow

- All feature 1 work lands on `feat/v2-image-support`; `public` stays untouched.
- Phases are ordered so each one compiles and its tests pass before the next.
- One risk (`crypto.subtle` availability in Wails) is verified early in
  Phase 3; one small spike (JS signing library) happens in Phase 3.
- Review gate: after each phase the working tree is left in a reviewable state;
  a final PR-style summary plus the manual checklist closes the feature.

## Shared test fixtures

New file: `testdata/image-url-fixtures.json`.

Go and JS cannot share one normalization function, so they share the **same
spec and the same test vectors**. The fixture contains URL-normalization cases
(base / prefix / key → expected URL, including slashes, whitespace, empty
prefix) and `publicBaseUrl` validation cases (valid/invalid: query, fragment,
userinfo, relative, non-http scheme). Go tests and vitest both read this file.

## Phase 1 — Go local vault API

New files: `api_images.go`, `api_images_test.go`. Edited: `server.go` (routes).

- `PUT /api/images/{key}` (authMiddleware, raw image body):
  - Key regex `^[a-f0-9]{64}\.(png|jpg|gif|webp|avif)$` (anchored, no slashes,
    canonical extensions only — **no `.jpeg`**).
  - `http.MaxBytesReader` (20 MiB).
  - Stream body through SHA-256 into a `.tmp` file; hex digest must equal the
    key's hash segment, else 400 (content↔key binding).
  - **`detectFormat(header)` is the single source of truth**: read a limited
    prefix (4 KiB), detect the format, require
    `canonicalExt(format) == requestedExt`, and derive `Content-Type` from the
    detected format:
    - PNG: bytes 0–7 exactly `89 50 4E 47 0D 0A 1A 0A`.
    - JPEG: bytes 0–2 `FF D8 FF`.
    - GIF: bytes 0–5 `GIF87a` or `GIF89a`.
    - WebP: bytes 0–3 `RIFF` and bytes 8–11 `WEBP`.
    - AVIF: ISO-BMFF — parse the first `ftyp` box within the prefix
      (boundary-check the box length); accept if the major brand **or any
      compatible brand** ∈ {`avif`, `avis`}. (Major-brand-only checks reject
      valid files where the brand sits in compatible brands; the prefix
      cap of 4 KiB bounds the read.)
  - Existing key: compare the stored object's size with the fresh tmp's size.
    Equal → 200 (idempotent). Different → the object violates the hash
    invariant (e.g. manually overwritten/truncated): log it, and **repair** by
    replacing with the verified tmp (remove-then-rename for Windows safety);
    failure to replace → 500. Never silently return 200 for a corrupt object.
  - New key → atomic rename (`os.CreateTemp` in the vault dir + `os.Rename`) →
    201.
- `GET /api/images/{key}` (authMiddleware): serve from `<dataDir>/.images/`;
  `Content-Type` from the detected-format map, `X-Content-Type-Options:
  nosniff`, `Cache-Control: private, max-age=31536000, immutable` (private —
  the endpoint is authenticated; content-hash URLs are immutable).
- Vault directory is `<dataDir>/.images/`. The main sidebar uses the v2 folder
  API, which already skips dot-prefixed directories; the legacy folder-tree
  builder (`buildFolderNodes`) does **not** skip them, so it must be aligned
  (skip dot-prefixed dirs) or the folder-picker dialog would show `.images/`.
  Also guard `createFolder` against reserving `.images`.
- Local mode needs no new config; it is always available.
- Follow CLAUDE.md conventions: `safePath`, atomic writes.

## Phase 2 — Go S3 proxy

New files: `s3.go`, `s3_test.go`. Edited: `main_cli.go` (flags/env),
`server.go`/`api_images.go` (handler branch + config endpoint).

- Config (existing cascade: flags → env → `.env`), env names
  `MEMODUMP_IMAGE_S3_*`. **Required fields: endpoint, bucket, accessKey,
  secretKey, publicBaseUrl.** Optional: region (default `us-east-1`), prefix
  (default `''`), forcePathStyle (default `true`; set `false` for AWS
  virtual-host style). S3 mode is active iff all required fields are present
  and non-empty.
- **URL normalization** (one Go function): trim whitespace; strip trailing `/`
  from `publicBaseUrl`; trim `prefix` and strip leading/trailing `/`; final URL
  = `publicBaseUrl + '/' + (prefix ? prefix + '/' : '') + key`. The JS build
  mirrors it (shared fixtures, above).
- **`publicBaseUrl` validation**: absolute `http(s)://` URL; no userinfo, no
  query, no fragment; a path is allowed (`https://cdn.example.com/images`).
  Pure frontend served over HTTPS rejects `http://` (mixed content); web/wails
  may allow `http://` for LAN self-hosting (separate `allowInsecure` flag).
- `PUT /api/images/{key}` branches: vault (Phase 1) or S3 proxy. The proxy
  keeps the same key validation + hash binding; `x-amz-content-sha256` is the
  hex digest we already computed. **After PUT, the proxy runs a read check**
  (HEAD or first-bytes GET of the public URL) and reports success only when the
  object is readable; partial failure returns an error with kind
  `verify-failed`.
- `GET /api/config` extended to
  `image: { provider: 'local' | 's3', bucket?, publicBaseUrl?, prefix?,
  configured: bool, editable: bool }` — no secrets leave the server.
  `editable` is false when the effective config comes from flags/env/`.env`.
- New `PUT /api/config/image` (authMiddleware): persists the S3 settings to
  the build's image-config file and applies them immediately — no restart.
  The path is a per-build package var using the same seam `sessionFile` uses:
  web → `<dataDir>/.image-config.json`; Wails → OS user-config dir
  `memodump/image-config.json`. Effective config resolves per-request with
  precedence flags → env → `.env` → file.
- Request body is
  `{ provider, endpoint, region, bucket, prefix, publicBaseUrl, accessKey,
  secretKey, forcePathStyle }` (size-limited, validated JSON, normalized on
  save). **Secret rotation rule**: creating a new S3 config requires both
  keys; editing the same config with empty `secretKey` means unchanged; if
  `endpoint`, `bucket`, or `accessKey` changes, the secret must be provided
  again (no mixing a new identity with the old secret). `provider: 'local'`
  (or an explicit clear-config action) reverts to the vault and clears
  secrets.
- Test connection (web/wails, server-side): PUT a probe under
  `.memodump-probe/<uuid>` with body matching the key's hash → anonymous GET
  the probe → verify 200 → best-effort DELETE. DELETE failure does **not** fail
  the test but warns that a probe object may remain; a policy allowing
  PUT/GET but not DELETE is a real, reported case.

### Decision: S3 client (confirmed)

Use **`minio-go`** (`github.com/minio/minio-go/v7`) for the Go-side S3
operations. Rationale: battle-tested SigV4/signing handling across the wide
range of S3-compatible endpoints, and it reduces trial-and-error cost compared
with a hand-rolled client. This is a new (non-Wails) dependency — accepted.

## Phase 3 — Frontend media pipeline

New files: `frontend/src/composables/mediaOutbox.js`,
`frontend/src/composables/useImageSettings.js`. Edited: `api.js`, `localApi.js`,
`MainView.vue` (bootstrap order + flush timer).

- `useImageSettings`: localStorage key `memodump_image_config` (kept separate
  from font settings; it may hold secrets in the pure-frontend build only).
  Provider: `off | s3` for the pure-frontend build; `local | s3` from
  `/api/config` for web/wails.
- **Bootstrap is a hard ordering constraint** — before the editor is mounted
  or any note markdown is set:

  ```js
  await imageSettings.init()
  await mediaOutbox.init()     // hydrate url → blob map from IndexedDB
  // ...then mount the editor / set markdown
  startFlushLoop()             // 30s + online event
  ```

  `MainView` already gates editor mounting behind `isInitializing`; init runs
  before that flag clears. This prevents the "editor renders the final URL
  before the proxy mapping exists" window.
- `mediaOutbox` implements the **canonical media staging flow** (staging only):
  - Client-side format detection mirrors the server (`detectFormat` on the
    first 4 KiB, canonical extensions, JPEG → `.jpg`); `File.type` is auxiliary
    only; the original filename extension never enters the key.
  - Validate size ≤ 20 MiB with a calm refusal message.
  - SHA-256 via `crypto.subtle` → key → normalized final URL → `await` the
    IndexedDB put (durable first) → create/reuse object URL → return URL.
  - Immediately attempt the upload; transition through
    `pending → uploaded → completed` per the lifecycle state machine.
  - Web/wails: one PUT to `/api/images/{key}`; the server (Go proxy) performs
    S3 read-verification, so 2xx means completed (vault) or PUT+verify success
    (S3).
  - Pure frontend: direct S3 PUT via the chosen signing library; then verify
    via anonymous GET or the rendered image's `load` event before `completed`.
- Record shape: `{ id, url, key, target, state, blob, attempts, nextAttemptAt,
  lastError, createdAt, uploadedAt? }` with
  `id = targetId + ':' + key` and the immutable `target` snapshot (no
  secrets). Provider/config switch → entries keep their destination; missing
  destination config → `config-required` state with a configuration action.
- **Object URL lifecycle (no DOM scanning)**: create on first `resolvePending`
  and cache; do **not** eagerly revoke after upload success; revoke all cached
  object URLs on note unload / editor destroy and on page unload (refresh
  naturally clears). The IndexedDB blob is removed only at `completed`. The
  small retained-memory cost is accepted for v1.
- **Startup hydration**: `mediaOutbox.init()` reads the pending/uploaded store
  into the `url → { blob, objectUrl? }` map (lazy object URLs) and derives the
  "N 张图片待上传" count.
- Backoff: `nextAttemptAt` gating, `30s → 2m → 5m → 15m → 30m` cap; 429 honors
  `Retry-After` when present. Error classification per the taxonomy table.
- `api.js` / `localApi.js`: `imageUpload(key, blob)` plus `config().image`
  plumbing.

### JS signing library spike

The pure-frontend direct PUT needs SigV4 in the browser. Do **not** hand-roll
the JS signer; spike two mature, tree-shakeable options and pick one:

- `aws4fetch` — small, fetch-native, endpoint-agnostic (works with
  S3-compatible services and path-style URLs).
- `@smithy/signature-v4` + `@smithy/protocol-http` — the official AWS signing
  primitives used by SDK v3, tree-shakeable without the full `@aws-sdk/client-s3`.

Selection criteria: bundle-size impact on the PWA, CORS behavior with the
chosen signing headers, and fit with `x-amz-content-sha256` (we already have
the digest). Our own unit tests cover config plumbing, URL construction and
the shared fixtures; the library's signing is exercised against the AWS
sig-v4-test-suite vectors once, as a guard.

### Provider "off" mode (pure frontend)

With no S3 configured there is no final URL, so URL-first cannot produce one.
In off mode, paste/drop of image files is refused with a calm in-app hint
("图片上传未配置") and no node is inserted; inserting image links continues to
work as today.

### Risk: `crypto.subtle` under Wails

`crypto.subtle` needs a secure context (https, localhost, or a Wails custom
scheme treated as secure). Verify availability in the Wails WebView early in
this phase. Fallback if unavailable: a Wails Go binding `App.HashSha256`
(Go's `crypto/sha256`, invoked through the existing `wailsjs` runtime) rather
than a second hand-maintained JS hash implementation.

## Phase 4 — Editor integration

Edited: `frontend/src/components/MilkdownEditor.vue`.

**Two insertion entry points, one staging flow — no double insert:**

- **Crepe upload button** (built-in file input): `onUpload(file)` →
  `stageAndUploadImage(file)`; **Crepe owns the node insertion**. This path
  cannot run in parallel with the paste handler.
- **Custom paste/drop** (Crepe does not handle image files): call
  `stageAndUploadImage(file)` ourselves, then insert the `image-block` node
  manually (ProseMirror transaction via `editor.action`; node from
  `schema.nodes['image-block'].create({ src })`). The paste/drop handler never
  re-enters `onUpload`.

`stageAndUploadImage(file) → finalURL` is the canonical media staging flow;
node insertion stays the caller's responsibility. Handlers must
`preventDefault` **and `stopPropagation`** for image files only: MainView's
main-content drop handler imports `.md/.txt` and alerts on anything else, and
it would otherwise swallow image drops. Non-image drops bubble so file import
keeps working. In off mode the drop is handled with the calm hint instead of
inserting a node.

- `proxyDomURL(url)` → `resolvePending(url)` (startup-hydrated map).
- `onImageLoadError` → mark genuinely broken image loads only (dead external
  links); no modal. For entries in `uploaded` state the display is swapped to
  the final URL, and a successful `load` event is the read verification that
  completes the entry. Permanent upload failures are not driven by img error
  events — the blob keeps rendering — they surface via the global "待上传"
  status with retry/configuration actions.
- Verified: the Wails asset server (`Handler: buildAPIMux()`) routes
  non-asset requests such as `/api/images/{key}` and `/api/config/image`
  through the shared mux, so the same backend code serves both entry points.
- No markdown flow changes: saved markdown already contains the final URL.

## Phase 5 — Settings panel + i18n

Edited: `SettingsPanel.vue`, `i18n/zh-CN.json`, `i18n/en.json`.

- Image section: provider status/selector, S3 form (endpoint / region / bucket /
  prefix / publicBaseUrl / accessKey / secretKey / forcePathStyle), test
  connection button, CORS template reference, public-read explanation, and the
  localStorage-key warning for the pure-frontend build.
- **Privacy warning, prominent in the S3 form**: "使用 S3 模式时，上传的图片
  可通过其链接公开访问；内容哈希不是访问控制，相同文件会生成相同链接。请勿
  上传需要私密保存的图片。"
- Web/wails: the S3 form is editable and persists via `PUT /api/config/image`;
  fields are masked after save; empty secret fields mean "unchanged" under the
  rotation rule (secret required again when endpoint/bucket/accessKey changes);
  a clear-config action reverts to the local vault. Read-only (with a source
  note) only when the effective config comes from flags/env/`.env`.
- **The image section is collapsed by default.** The section header always
  shows a one-line mode summary (e.g. "本地图库" / "S3: <bucket>" / "已关闭");
  expanding reveals the provider selector and — only when S3 is selected — the
  credential form, test connection button and CORS guidance. Collapsed state is
  not persisted (defaults to collapsed on open).
- Test connection (pure frontend): probe PUT under `.memodump-probe/<uuid>`
  with hash-matched body → anonymous GET probe → best-effort DELETE (DELETE
  failure warns but does not fail the test). Surface CORS preflight vs.
  credential errors vs. private-bucket errors distinctly, using the error
  taxonomy.

## Phase 6 — Docs & manual tests

- README: per-build image support matrix, CORS configuration template, public
  read + privacy requirements, security notes (key binding, bucket
  permissions, SVG exclusion rationale, orphan objects, hash-URL privacy
  semantics).
- README: document that vault-mode image URLs in markdown are relative
  (`/api/images/…`) and therefore resolve only inside the app origin — the
  portability tradeoff of self-hosting images.
- `docs/manual-testing.md`: paste online/offline, vault and S3 modes, reload
  persistence of pending images, lifecycle states, raw-mode URL display,
  backoff/retry behavior, Wails smoke test.
- Full verification: `go vet ./...`, `go test ./...`, both frontend builds
  (`npm run build`, `npm run build:local`).

## Out of scope for v1

WebDAV, client-side compression, per-image pending badges, SVG support,
private buckets, pending-entry migration across config changes, quotas/rate
limits, the Crepe alt-text fix, and migration tooling for existing notes.
