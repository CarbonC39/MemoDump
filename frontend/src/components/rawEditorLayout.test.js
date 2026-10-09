import { describe, expect, it } from 'vitest'
import { fitTextareaToContent } from './rawEditorLayout'

describe('fitTextareaToContent', () => {
  it('clears the previous height before measuring the full content', () => {
    const style = { height: '120px' }
    const element = {
      style,
      get scrollHeight() {
        expect(style.height).toBe('0')
        return 480
      },
    }

    fitTextareaToContent(element)

    expect(style.height).toBe('480px')
  })

  it('accepts an unavailable textarea during mode transitions', () => {
    expect(() => fitTextareaToContent(null)).not.toThrow()
  })
})
