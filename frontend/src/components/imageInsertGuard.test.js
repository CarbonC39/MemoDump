import { describe, expect, it } from 'vitest'
import { imageInsertStillCurrent } from './imageInsertGuard'

describe('image insertion document guard', () => {
  it('allows insertion only into the document that started staging', () => {
    expect(imageInsertStillCurrent({
      documentVersion: 4,
      currentDocumentVersion: 4,
      activeDocumentVersion: 4,
      destroyed: false,
    })).toBe(true)
    expect(imageInsertStillCurrent({
      documentVersion: 4,
      currentDocumentVersion: 5,
      activeDocumentVersion: 5,
      destroyed: false,
    })).toBe(false)
    expect(imageInsertStillCurrent({
      documentVersion: 4,
      currentDocumentVersion: 4,
      activeDocumentVersion: 4,
      destroyed: true,
    })).toBe(false)
    expect(imageInsertStillCurrent({
      documentVersion: 4,
      currentDocumentVersion: 4,
      activeDocumentVersion: 4,
      destroyed: false,
      active: false,
    })).toBe(false)
  })
})
