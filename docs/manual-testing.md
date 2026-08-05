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

## Completion criteria

A release passes this checklist when:

- All applicable steps pass in the required test matrix.
- Saved content survives reloads and mode switches without alteration.
- No note receives content from another note.
- Raw mode remains available when the rich editor is slow or unavailable.
- There are no unexpected console errors.
- Any skipped platform or scenario is recorded with a reason.
