import { describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import { useFolderActions } from './useFolderActions'

function setup(overrides = {}) {
  const currentFolder = ref('projects/old/child')
  const api = {
    createFolder: vi.fn().mockResolvedValue({}),
    renameFolder: vi.fn().mockResolvedValue({}),
    deleteFolder: vi.fn().mockResolvedValue({}),
  }
  const deps = {
    api,
    currentFolder,
    loadAll: vi.fn().mockResolvedValue(undefined),
    loadFolderNode: vi.fn().mockResolvedValue(undefined),
    refreshRootFolders: vi.fn().mockResolvedValue(undefined),
    showPrompt: vi.fn(),
    showConfirm: vi.fn().mockResolvedValue(true),
    t: key => key,
    updateUrl: vi.fn(),
    notify: vi.fn(),
    ...overrides,
  }
  return { actions: useFolderActions(deps), deps, api, currentFolder }
}

describe('useFolderActions', () => {
  it('migrates the active descendant path after a rename', async () => {
    const { actions, deps, api, currentFolder } = setup()
    deps.showPrompt.mockResolvedValue('new')

    await actions.promptRenameFolder('projects/old')

    expect(api.renameFolder).toHaveBeenCalledWith('projects/old', 'new')
    expect(currentFolder.value).toBe('projects/new/child')
    expect(deps.loadAll).toHaveBeenCalled()
    expect(deps.updateUrl).toHaveBeenCalled()
  })

  it('refreshes only the affected parent after nested creation', async () => {
    const { actions, deps, api } = setup()
    deps.showPrompt.mockResolvedValue('child')

    await actions.promptNewFolder('projects')

    expect(api.createFolder).toHaveBeenCalledWith('projects/child')
    expect(deps.loadFolderNode).toHaveBeenCalledWith('projects', { force: true })
    expect(deps.refreshRootFolders).not.toHaveBeenCalled()
  })

  it('keeps a folder when destructive confirmation is cancelled', async () => {
    const { actions, deps, api } = setup()
    deps.showConfirm.mockResolvedValue(false)

    await actions.deleteFolder('projects/old')

    expect(api.deleteFolder).not.toHaveBeenCalled()
  })
})
