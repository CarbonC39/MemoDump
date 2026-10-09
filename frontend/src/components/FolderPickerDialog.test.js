// @vitest-environment happy-dom
import { beforeEach, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import FolderPickerDialog from './FolderPickerDialog.vue'
import { useI18n } from '../i18n'

const folders = [
  { path: 'work', name: 'work', depth: 0 },
  { path: 'work/project', name: 'project', depth: 1 },
  { path: 'archive', name: 'archive', depth: 0 },
]

describe('FolderPickerDialog', () => {
  beforeEach(() => useI18n().setLocale('en'))

  it('shows the selected destination and distinct restrained folder tones', () => {
    const wrapper = mount(FolderPickerDialog, {
      props: { visible: true, selected: 'work/project', folders },
    })

    expect(wrapper.find('.destination-copy strong').text()).toBe('Root / work / project')
    expect(wrapper.find('.folder-tone-0').exists()).toBe(true)
    expect(wrapper.find('.folder-tone-1').exists()).toBe(true)
    expect(wrapper.find('.folder-picker-item.active .selected-check').exists()).toBe(true)
    expect(wrapper.find('.btn-new-folder').text()).toContain('project')
  })

  it('disables a no-op move but allows selecting the current folder', () => {
    const move = mount(FolderPickerDialog, {
      props: { visible: true, selected: 'work', currentFolder: 'work', mode: 'move', folders },
    })
    expect(move.find('.prompt-actions .btn-primary').attributes('disabled')).toBeDefined()

    const select = mount(FolderPickerDialog, {
      props: { visible: true, selected: 'work', currentFolder: 'work', mode: 'select', folders },
    })
    expect(select.find('.prompt-actions .btn-primary').attributes('disabled')).toBeUndefined()
    expect(select.text()).toContain('Select This Folder')
  })

  it('uses accessible buttons for folder selection', async () => {
    const wrapper = mount(FolderPickerDialog, {
      props: { visible: true, selected: '', folders },
    })
    const project = wrapper.findAll('.folder-picker-item').find(item => item.text().includes('project'))
    await project.trigger('click')
    expect(wrapper.emitted('update:selected')).toContainEqual(['work/project'])
  })
})
