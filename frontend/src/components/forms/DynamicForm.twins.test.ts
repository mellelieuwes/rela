// DynamicForm under twin ownership (twins stage 1): an external system that
// owns the body makes the body editor read-only with a hint, and an owned
// property renders read-only with the server's reason as its help.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { mount, flushPromises, type VueWrapper } from '@vue/test-utils'
import { useSchemaStore, useEntitiesStore } from '@/stores'
import DynamicForm from './DynamicForm.vue'
import MilkdownEditor from './milkdown/MilkdownEditor.vue'
import type { Entity } from '@/types'

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useRoute: () => ({ query: {}, params: {}, path: '/form/scenario-form' }),
  onBeforeRouteLeave: vi.fn(),
}))

const FORM = {
  id: 'scenario-form',
  entity: 'scenario',
  fields: [
    { property: 'title', label: 'Title' },
    { property: 'notes', label: 'Notes' },
  ],
}

const mounted: VueWrapper[] = []

afterEach(() => {
  mounted.splice(0).forEach((w) => w.unmount())
})

async function mountEdit(over: Partial<Entity>) {
  const schema = useSchemaStore()
  schema.forms.set(FORM.id, FORM as never)
  schema.entityTypes.set('scenario', {
    name: 'scenario',
    label: 'Scenario',
    properties: { title: { type: 'string' }, notes: { type: 'string' } },
  } as never)
  schema.loaded = true
  const entity: Entity = {
    id: 'SC-015',
    type: 'scenario',
    properties: { title: 'Checkout succeeds', notes: '' },
    content: 'Given a cart\n',
    _actions: { update: true },
    _fields: {},
    _redacted: [],
    ...over,
  }
  vi.spyOn(useEntitiesStore(), 'fetchEntity').mockResolvedValue(entity)
  const wrapper = mount(DynamicForm, {
    props: { formId: FORM.id, entityId: 'SC-015' },
    global: {
      stubs: {
        RouterLink: true,
        MarkdownEditor: true,
        RelationPicker: true,
        RelationCards: true,
        AutoSaveIndicator: true,
        SidePanel: true,
      },
    },
  })
  mounted.push(wrapper)
  await flushPromises()
  return wrapper
}

describe('DynamicForm twin ownership', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.restoreAllMocks()
  })

  it('makes the body editor read-only with a hint when the body is owned', async () => {
    const w = await mountEdit({ content_writable: false })
    expect(w.findComponent(MilkdownEditor).props('readonly')).toBe(true)
    expect(w.find('.content-owned-hint').exists()).toBe(true)
  })

  it('leaves the body editable when the server does not say otherwise', async () => {
    const w = await mountEdit({})
    expect(w.findComponent(MilkdownEditor).exists()).toBe(true)
    expect(w.findComponent(MilkdownEditor).props('readonly')).toBe(false)
    expect(w.find('.content-owned-hint').exists()).toBe(false)
  })

  it("shows an owned field's reason and locks it", async () => {
    const w = await mountEdit({
      _fields: { title: { writable: false, reason: 'Owned by basecamp' } },
    })
    expect(w.find('#field-title').attributes('disabled')).toBeDefined()
    expect(w.text()).toContain('Owned by basecamp')
    // The reason belongs to the owned field only.
    expect(w.find('#field-notes').attributes('disabled')).toBeUndefined()
  })
})
