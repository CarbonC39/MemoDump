import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../api', () => ({
  default: {
    imageConfigGet: vi.fn(),
    imageConfigSave: vi.fn(),
    imageConfigTest: vi.fn(),
  },
}))

import apiClient from '../api'
import {
  currentTarget,
  getImageSettings,
  initImageSettings,
  refreshImageSettings,
  saveImageConfig,
} from './useImageSettings'

describe('server image settings round trip', () => {
  beforeEach(() => {
    apiClient.imageConfigGet.mockResolvedValue({
      data: {
        provider: 's3',
        configured: true,
        editable: true,
        endpoint: 'https://s3.example.com',
        region: 'us-west-2',
        bucket: 'notes',
        prefix: 'images',
        publicBaseUrl: 'https://cdn.example.com',
        accessKey: 'ak',
        forcePathStyle: false,
        targetId: 's3:revision-one',
      },
    })
  })

  it('hydrates editable non-secret fields and uses the server target revision', async () => {
    await initImageSettings()
    expect(getImageSettings()).toMatchObject({
      endpoint: 'https://s3.example.com',
      region: 'us-west-2',
      accessKey: 'ak',
      secretKey: '',
      forcePathStyle: false,
    })
    expect(currentTarget().id).toBe('s3:revision-one')

    apiClient.imageConfigGet.mockResolvedValueOnce({
      data: { provider: 'local', configured: false, editable: true, targetId: 'local' },
    })
    await refreshImageSettings()
    expect(currentTarget()).toMatchObject({ id: 'local', provider: 'local' })

    apiClient.imageConfigSave.mockResolvedValue({
      data: { provider: 'local', configured: false, editable: true, targetId: 'local' },
    })
    await saveImageConfig({ provider: 'local', cleanup: { enabled: false } })
    expect(currentTarget()).toMatchObject({ id: 'local', provider: 'local' })
  })
})
