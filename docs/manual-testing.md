# Manual Testing

Use this checklist before a release or after changes to the editor, persistence,
offline behavior, or navigation. Complete it in a temporary data directory so
test notes cannot overwrite personal data.

Automated tests remain the primary regression guard. This checklist covers
browser behavior, visual state, focus, and multi-step workflows that are harder
to verify with unit tests.

## Test matrix

Run the core checklist in:

- A current Chromium-based browser at desktop width.
- A narrow viewport (approximately 390 px wide).
- The Wails desktop build when a release changes desktop integration.

When practical, repeat the editor loading checks in Firefox or Safari.

Record the application version, platform, browser, viewport, and any failed
step in the release notes or issue.

## Preparation

1. Start MemoDump with a new temporary data directory.
2. Open the browser developer console and keep it visible during testing.
3. Create one note containing:

   ````markdown
   # Editor smoke test

   Plain text with **bold**, *italic*, and [a link](https://example.com).

   - [ ] First task
   - [x] Completed task

   ```js
   console.log('test')
   ```
   ````

4. Save the note and reload the page.
5. Confirm there are no unexpected errors in the console.

## Core editor checklist

### Existing note

1. Open the prepared note in the rich editor.
2. Confirm the heading, formatting, task list, link, and code block render.
3. Switch to the raw editor.
4. Confirm the Markdown textarea appears immediately and contains the complete
   source.
5. Edit the heading and add a line in raw mode.
6. Switch to the rich editor.
7. Confirm both raw-mode changes appear and the rich editor is usable.
8. Make another edit in the rich editor.
9. Switch to raw mode and confirm the Markdown reflects that edit.
10. Switch between both modes two more times.
11. Confirm the editor never becomes blank, shows overlapping editors, loses
    focus permanently, or loses content.
12. Save, reload the page, and confirm all changes persist.

### New and empty note

1. Create a new note.
2. Switch to raw mode before entering any content.
3. Enter a heading and body.
4. Switch to the rich editor and confirm the content appears.
5. Clear all content and switch between modes again.
6. Confirm both editors remain usable when the note is empty.
7. Save, reload, and confirm the empty or edited state is preserved as expected.

### Consecutive notes

1. Create two notes with visibly different content.
2. Open the first note and switch to raw mode.
3. Open the second note and confirm only its content is shown.
4. Switch to the rich editor and return to the first note.
5. Confirm neither note displays stale content from the other.
6. Edit each note, save, reload, and verify both independently.

## Rich editor loading and fallback

Use browser developer tools to throttle the network or block the generated
`MilkdownEditor` JavaScript chunk.

1. Open a note in rich mode with slow network throttling enabled.
2. Confirm a loading state is displayed without hiding the application controls.
3. Wait for the raw-editor fallback action to appear.
4. Select it and confirm the raw editor opens with the complete Markdown source.
5. Edit and save the note in raw mode.
6. Disable throttling or remove the blocked request.
7. Switch to the rich editor and confirm it loads the latest content.
8. Repeat with the editor chunk blocked so loading fails.
9. Confirm the failure state offers retry and raw-editor actions.
10. Confirm retry succeeds after unblocking the request.
11. Confirm no edit is lost during either fallback path.

## Persistence and offline behavior

1. Edit a saved note and wait for autosave to finish.
2. Reload and confirm the change persists.
3. Disable the network while a note is open.
4. Make edits in both raw and rich modes.
5. Confirm the application reports the offline or queued state without blocking
   further editing.
6. Reload only if the test environment is known to preserve the offline draft.
7. Restore the network and wait for synchronization.
8. Reload and confirm the latest content appears exactly once.
9. Confirm switching notes during save does not copy content into the wrong note.

## Navigation and interruption safety

1. Make an unsaved edit, then attempt to open another note, search, go back, and
   open settings.
2. Confirm each action either preserves the draft or presents the expected
   discard confirmation.
3. Cancel the confirmation and verify the edit is still present.
4. Accept it and verify navigation completes without modifying the destination
   note.
5. Use browser back and forward navigation while alternating editor modes.
6. Confirm the correct note and mode remain usable after every navigation.

## Responsive and accessibility smoke checks

1. Repeat the core editor checklist at a narrow viewport.
2. Confirm the editor, header actions, fallback action, and mode toggle remain
   visible and do not overlap.
3. Zoom the page to 200% and confirm editing and mode switching are still
   possible.
4. Navigate the editor header and mode toggle using only the keyboard.
5. Confirm focus is visible and keyboard activation works.
6. Confirm loading and failure states communicate their status without rapid
   flashing or disruptive layout shifts.
7. Test both light and dark themes and confirm raw text, selection, placeholders,
   and focus indicators remain readable.

## Image support checklist

Run against the local vault by default, and again with an S3 config (fake or
real) if available.

1. Paste a PNG into the rich editor; confirm an image appears and the markdown
   contains `/api/images/<sha256>.png` (vault) or the configured public URL
   (S3).
2. Paste a JPEG, GIF, WebP and AVIF; confirm each uploads and renders. Confirm
   a file whose content does not match its extension is rejected with a calm
   notice.
3. Paste a file larger than 20 MiB and a non-image file; confirm both are
   refused without a modal.
4. Drop an image onto the editor; confirm it inserts at the drop position and
   that dropping a `.md` file elsewhere still imports it (no alert regression).
5. Paste an image while offline; confirm it renders from the local blob, the
   app shows a pending state, and it uploads automatically after reconnecting
   (or via the retry action).
6. Reload the page while an image is pending; confirm it still renders and
   eventually uploads.
7. Paste a large image and switch notes or switch to raw mode before hashing
   finishes; confirm no image node is inserted into the new editing context.
8. Save the note, reload, and confirm the image loads from its final URL.
9. Open the raw editor and confirm the image is an ordinary markdown image link
   with the final URL.
10. In S3 mode, configure a bucket that rejects anonymous reads and confirm the
   test connection reports it (and that uploads surface a readable error).
11. In the settings panel, confirm the Images section is collapsed by default,
   the mode summary is accurate, the privacy notice is visible, and saving a
   config with empty secret fields preserves the existing secret. Reload the
   page and confirm the same config can still be edited and saved.
12. Change the server image destination while an upload is pending and confirm
   the old entry becomes config-required instead of uploading to the new target.
13. Confirm the Wails build serves vault images (`/api/images/...`) and that
   the image settings are editable there.
14. Confirm no `.tmp` files or `uploads` artifacts remain in the vault
    directory after uploads.

## Cloud sync checklist (R5, Wails Go engine)

Run against a real or fake S3-compatible provider (MinIO) with two Wails
installations — two machines or two OS accounts, since the Wails sync state
lives in the OS application-data directory (per user, outside the vault).
Record the application build, provider, date, and result for each step on
Windows, macOS, and Linux.

1. **Startup and periodic convergence.** On replica A, create a note BEFORE
   connecting sync, then **Connect**: connecting triggers an immediate run, so the note
   uploads without further action. (If you create the note AFTER connecting, it may
   wait until the next five-minute interval.) On replica B (already connected),
   the note downloads within the next automatic interval. Confirm the settings
   panel remains connected and reports the latest successful sync.
2. **Manual / automatic single-flight.** Trigger **Sync now** while an automatic
   run is in progress and confirm they serialize (no overlapping cycles, no
   duplicate conflict notes). Shut the app down mid-cycle and confirm no
   background sync work remains and the app exits cleanly.
3. **Concurrent edit and both edit/delete conflicts.** Edit the same note on A
   and B; edit on A while deleting on B; delete on A while editing on B. Confirm
   all edited Markdown survives as exactly one conflict note each (never
   duplicates).
4. **Pulled deletion, recovery, restore.** Delete a note on A; confirm B applies
   the deletion and writes a durable recovery copy, and that Restore in the
   settings panel brings the note back at its original path.
5. **Remote update while the editor is clean and while it is dirty.** Edit a
   note on A while B has it open (clean): B's editor adopts the new revision.
   Repeat with an unsaved edit in B's editor: the buffer is not replaced, a
   non-blocking "synced version changed" notice appears, and the existing
   revision CAS prevents overwrite.
6. **Unicode/case-portable paths and state persistence.** Create notes with
   Unicode names and case-differing names on case-insensitive platforms; confirm
   both converge. Restart the app and confirm identity, the connection pin, and
   recovery copies survive via the persisted OS application-data state.
7. **Failure behavior.** Revoke the provider credentials (auth failure) and
   confirm the status shows the redacted reason and automatic sync pauses;
   restore them and confirm a manual **Sync now** succeeds and clears the pause.
   Disconnect the network mid-run and confirm a transient error is shown and a
   later automatic run retries. Confirm **Disconnect** stops future attempts, and
   **Connect different storage…** switches to a second repository deliberately.

## Cloud sync checklist (R6 browser engine)

Run against the **Pure frontend / PWA build** (`cd frontend && npm run
build:local`, serving `dist` over HTTPS) and a real S3-compatible provider
(MinIO or a private bucket) with an **isolated prefix**. Keep the browser
developer console visible. Record the build, provider, browser, date, and
result for each step.

> The browser build must run in a **secure context**: use HTTPS, or
> `http://localhost` during development. Web Locks and `crypto.subtle` are
> unavailable on a plain LAN HTTP address, so a second device/profile must use
> an HTTPS origin — do not point it at `http://<lan-ip>:port`.

1. **Opt-in real S3 run.** Configure the note-sync form (endpoint, region,
   bucket, prefix, access/secret key, path style), then click **Connect**. The
   combined save/capability check must succeed, its probe object must be cleaned
   up in the bucket, and the first sync cycle must run immediately.
2. **CORS template.** Configure the bucket's CORS with the template from the
   settings panel (methods + `Authorization`/`Content-Type`/`x-amz-*`/
   `If-Match`/`If-None-Match` headers, exposing `ETag` and `Retry-After`).
   Confirm signed reads/writes and the capability probe pass from the app
   origin and fail (with a clear error) when a header or the ETag exposure is
   removed.
3. **PWA↔PWA convergence.** On replica A, create a note, enable sync, and
   confirm it uploads. On replica B (a second browser profile), enable sync
   and confirm the note downloads. Edit on A, then confirm B picks the change
   up on the next automatic interval (page kept open). Repeat with the page
   hidden on B and confirm nothing runs while hidden and the change arrives
   when B becomes visible again.
4. **Wails↔PWA interoperability.** Run the Wails desktop build configured
   against the same bucket/prefix and a PWA replica: create a note on each side
   and confirm both converge through the same repository with no conflict
   notes and byte-compatible records.
5. **Dirty-editor protection.** Edit a note on A while B has the same note open
   with an **unsaved change**: B's buffer is never replaced or closed, a
   non-blocking "synced version changed" notice appears, and saving later
   either reconciles or surfaces a visible conflict (never a silent overwrite).
   Repeat with B offline (queued outbox) and with B in a conflict state.
6. **Conflict / deletion / recovery.** Edit the same note on A and B (exactly
   one conflict note, no duplicates); edit on A while deleting on B; delete on
   A while editing on B. Delete a note on A and confirm B writes a durable
   recovery copy and Restore brings it back at its original path.
7. **Offline / transient recovery.** Disconnect the network on B, make local
   edits, reconnect, and confirm the queued edits upload on the next run. Force
   a transient provider error and confirm the in-memory backoff (`1m, 2m, 5m,
   10m, 30m`) shows a later scheduled run and honors a larger provider
   `Retry-After`.
8. **Failure pause / reconnect / repository mismatch.** Revoke the credentials on B:
   automatic sync pauses with the redacted reason; restore them and a manual
   **Sync now** clears the pause. Confirm **Connect different storage…** clears
   the pin and snapshot but keeps notes and recovery copies, and that connecting
   against a different repository (or a lost `repo.json`) is refused until then.
9. **Clearing site data.** On a disposable replica, clear the site's IndexedDB
   (devtools → Application → Storage) and confirm the PWA re-enables as a fresh
   replica against the same repository and re-downloads the remote notes.
10. **Page-lifetime guarantee.** Close the PWA tab mid-cycle and confirm no
    console errors and no background work; reopening resumes with the ordinary
    startup run after 10 seconds.

## Completion criteria

A release passes this checklist when:

- All applicable steps pass in the required test matrix.
- Saved content survives reloads and mode switches without alteration.
- No note receives content from another note.
- Raw mode remains available when the rich editor is slow or unavailable.
- There are no unexpected console errors.
- Any skipped platform or scenario is recorded with a reason.
