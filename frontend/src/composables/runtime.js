// Runtime capability matrix (R6.0, R6.5). Cloud-sync availability is a property
// of the owning runtime, not just of the frontend build type:
//
//   Wails desktop            -> available (reviewed Go R0–R5 engine + scheduler)
//   CLI Web server (browser) -> unavailable (all clients share one server vault)
//   Pure frontend / PWA      -> available (R6.5 in-page browser engine)
//
// The Wails runtime is detected by the window.go bridge the Wails runtime
// injects. The value is a module-level getter so unit suites can select each
// mode explicitly via setCloudSyncAvailable, mirroring how the Go runtime
// matrix tests override cloudSyncCapable.
export const isLocalBuild = import.meta.env.VITE_LOCAL === '1'
export const isWailsApp = typeof window !== 'undefined' && typeof window.go !== 'undefined'

let _cloudSyncAvailable = isWailsApp || isLocalBuild

export function cloudSyncAvailable() {
  return _cloudSyncAvailable
}

export function setCloudSyncAvailable(value) {
  _cloudSyncAvailable = value
}
