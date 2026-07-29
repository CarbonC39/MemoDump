# Review Follow-ups

## Scope

1. Move note ordering into the v2 list API so both ascending and descending
   pagination are globally correct.
2. Delay the Milkdown raw-mode fallback without changing genuine load-error
   handling.
3. Fix offline replay so timestamp-named notes are not renamed accidentally.
4. Preserve or independently load folder destinations needed by the picker.
5. Measure the save path and remove avoidable UI/network latency while keeping
   conflict-safe, single-flight writes.

## Verification

- Add focused Go and frontend tests for sorting, replay and folder loading.
- Run `go test ./...`, the full frontend test suite, and both normal/local
  production builds.
