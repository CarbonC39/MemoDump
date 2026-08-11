import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// runtime.js reads window.go and import.meta.env.VITE_LOCAL at module
// evaluation time, so every mode is tested by re-importing it with a fresh
// module cache and the simulated environment in place.
async function loadRuntime() {
  vi.resetModules()
  return import('./runtime')
}

describe('runtime capability matrix (R6.0/R6.5)', () => {
  beforeEach(() => {
    vi.unstubAllEnvs()
    delete globalThis.window
  })

  afterEach(() => {
    vi.unstubAllEnvs()
    delete globalThis.window
  })

  it('Wails desktop: window.go present on the server build -> sync available', async () => {
    globalThis.window = { go: {} }
    const runtime = await loadRuntime()
    expect(runtime.isLocalBuild).toBe(false)
    expect(runtime.isWailsApp).toBe(true)
    expect(runtime.cloudSyncAvailable()).toBe(true)
  })

  it('CLI Web server: browser without window.go on the server build -> sync unavailable', async () => {
    const runtime = await loadRuntime()
    expect(runtime.isLocalBuild).toBe(false)
    expect(runtime.isWailsApp).toBe(false)
    expect(runtime.cloudSyncAvailable()).toBe(false)
  })

  it('Pure frontend/PWA: VITE_LOCAL=1 -> sync available via the R6.5 browser engine', async () => {
    vi.stubEnv('VITE_LOCAL', '1')
    const runtime = await loadRuntime()
    expect(runtime.isLocalBuild).toBe(true)
    expect(runtime.isWailsApp).toBe(false)
    expect(runtime.cloudSyncAvailable()).toBe(true)
  })

  it('setCloudSyncAvailable overrides the detected capability explicitly', async () => {
    const runtime = await loadRuntime()
    expect(runtime.cloudSyncAvailable()).toBe(false)
    runtime.setCloudSyncAvailable(true)
    expect(runtime.cloudSyncAvailable()).toBe(true)
  })
})
