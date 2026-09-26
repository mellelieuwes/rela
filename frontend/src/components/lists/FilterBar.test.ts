import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import type { VueWrapper } from '@vue/test-utils'
import FilterBar from './FilterBar.vue'
import EntityTargetSelect from '@/components/common/EntityTargetSelect.vue'
import { useSchemaStore } from '@/stores/schema'
import type { Entity, EntityType, ListConfig, RelationType, ListMeta } from '@/types'

// Read the EntityTargetSelect child's props without depending on vue-tsc's
// SFC prop-key inference through findComponent (which this project's config
// doesn't surface). We only assert on a couple of known props.
function targetSelectProps(wrapper: VueWrapper): { mode?: string; modelValue?: string } {
  return wrapper.findComponent(EntityTargetSelect).props() as { mode?: string; modelValue?: string }
}

// FilterBar loads relation-filter candidates via entitiesStore.fetchList on
// mount and surfaces load failures via uiStore.error. Mock both.
const fetchListMock = vi.fn()
const uiErrorMock = vi.fn()
vi.mock('@/stores', async (orig) => {
  const actual = await orig<typeof import('@/stores')>()
  return {
    ...actual,
    useEntitiesStore: () => ({ fetchList: fetchListMock }),
    useUIStore: () => ({ error: uiErrorMock }),
  }
})

const META: ListMeta = { total: 0, page: 1, per_page: 100, has_more: false }

function seedRelationTypes(defs: Record<string, Partial<RelationType>>) {
  const store = useSchemaStore()
  store.relationTypes = new Map(
    Object.entries(defs).map(([name, def]) => [name, { label: name, from: [], to: [], ...def }])
  )
}

function seedRelationType(name: string, def: Partial<RelationType>) {
  seedRelationTypes({ [name]: def })
}

function persoon(id: string, title: string): Entity {
  return { id, type: 'persoon', _title: title, properties: {} }
}

// fetchList returns a list-response shape; seed `data` per call.
function stubCandidates(entities: Entity[]) {
  fetchListMock.mockResolvedValue({ data: entities, meta: META })
}

const TAAK_TYPE: EntityType = { label: 'Taak', properties: {} }

function relationListConfig(direction?: 'incoming' | 'outgoing'): ListConfig {
  return {
    entity: 'taak',
    columns: [],
    filter_controls: [{ relation: 'verantwoordelijk_voor', direction, label: 'Verantwoordelijke' }],
  }
}

describe('FilterBar — relation filter controls', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    fetchListMock.mockReset()
    uiErrorMock.mockReset()
  })

  it('renders an EntityTargetSelect for a relation filter control', async () => {
    seedRelationType('verantwoordelijk_voor', { from: ['persoon'], to: ['taak'] })
    stubCandidates([persoon('PERS-JV', 'Jeroen Vloothuis')])

    const wrapper = mount(FilterBar, {
      props: { config: relationListConfig('incoming'), entityType: TAAK_TYPE, filters: {} },
    })
    await flushPromises()

    expect(wrapper.findComponent(EntityTargetSelect).exists()).toBe(true)
  })

  it('incoming direction fetches candidates from the relation `from` types', async () => {
    seedRelationType('verantwoordelijk_voor', { from: ['persoon'], to: ['taak'] })
    stubCandidates([persoon('PERS-JV', 'Jeroen Vloothuis')])

    mount(FilterBar, {
      props: { config: relationListConfig('incoming'), entityType: TAAK_TYPE, filters: {} },
    })
    await flushPromises()

    expect(fetchListMock).toHaveBeenCalledWith('persoon', { per_page: 100 })
  })

  it('outgoing (default) direction fetches candidates from the relation `to` types', async () => {
    seedRelationType('verantwoordelijk_voor', { from: ['persoon'], to: ['taak'] })
    stubCandidates([])

    mount(FilterBar, {
      props: { config: relationListConfig('outgoing'), entityType: TAAK_TYPE, filters: {} },
    })
    await flushPromises()

    expect(fetchListMock).toHaveBeenCalledWith('taak', { per_page: 100 })
  })

  it('renders select mode at or below the threshold (10 candidates)', async () => {
    seedRelationType('verantwoordelijk_voor', { from: ['persoon'], to: ['taak'] })
    stubCandidates(Array.from({ length: 10 }, (_, i) => persoon(`P${i}`, `Person ${i}`)))

    const wrapper = mount(FilterBar, {
      props: { config: relationListConfig('incoming'), entityType: TAAK_TYPE, filters: {} },
    })
    await flushPromises()

    expect(targetSelectProps(wrapper).mode).toBe('select')
  })

  it('renders typeahead mode above the threshold (11 candidates)', async () => {
    seedRelationType('verantwoordelijk_voor', { from: ['persoon'], to: ['taak'] })
    stubCandidates(Array.from({ length: 11 }, (_, i) => persoon(`P${i}`, `Person ${i}`)))

    const wrapper = mount(FilterBar, {
      props: { config: relationListConfig('incoming'), entityType: TAAK_TYPE, filters: {} },
    })
    await flushPromises()

    expect(targetSelectProps(wrapper).mode).toBe('typeahead')
  })

  it('committing a value emits the filter keyed by the relation name', async () => {
    seedRelationType('verantwoordelijk_voor', { from: ['persoon'], to: ['taak'] })
    stubCandidates([persoon('PERS-JV', 'Jeroen Vloothuis')])

    const wrapper = mount(FilterBar, {
      props: { config: relationListConfig('incoming'), entityType: TAAK_TYPE, filters: {} },
    })
    await flushPromises()

    wrapper.findComponent(EntityTargetSelect).vm.$emit('update:modelValue', 'Jeroen Vloothuis')
    await flushPromises()

    const emits = wrapper.emitted('filter')
    expect(emits).toBeTruthy()
    // Wire key is the bare relation name; value is the bare title.
    expect(emits?.[emits.length - 1]).toEqual([
      { verantwoordelijk_voor: { value: 'Jeroen Vloothuis' } },
    ])
  })

  it('empty commit clears the filter (omits the key)', async () => {
    seedRelationType('verantwoordelijk_voor', { from: ['persoon'], to: ['taak'] })
    stubCandidates([persoon('PERS-JV', 'Jeroen Vloothuis')])

    const wrapper = mount(FilterBar, {
      props: {
        config: relationListConfig('incoming'),
        entityType: TAAK_TYPE,
        filters: { verantwoordelijk_voor: { value: 'Jeroen Vloothuis' } },
      },
    })
    await flushPromises()

    wrapper.findComponent(EntityTargetSelect).vm.$emit('update:modelValue', '')
    await flushPromises()

    const emits = wrapper.emitted('filter')
    expect(emits?.[emits.length - 1]).toEqual([{}])
  })

  it('a deep-linked relation filter value is passed through as the committed value', async () => {
    seedRelationType('verantwoordelijk_voor', { from: ['persoon'], to: ['taak'] })
    stubCandidates([persoon('PERS-JV', 'Jeroen Vloothuis')])

    const wrapper = mount(FilterBar, {
      props: {
        config: relationListConfig('incoming'),
        entityType: TAAK_TYPE,
        filters: { verantwoordelijk_voor: { value: 'Jeroen Vloothuis' } },
      },
    })
    await flushPromises()

    expect(targetSelectProps(wrapper).modelValue).toBe('Jeroen Vloothuis')
  })

  it('the mode flips select→typeahead when the loaded set exceeds the threshold', async () => {
    seedRelationType('verantwoordelijk_voor', { from: ['persoon'], to: ['taak'] })
    // Resolve the fetch only when we choose to, so we can observe the pre-load
    // state and the post-load flip.
    let resolveFetch!: (v: { data: Entity[]; meta: ListMeta }) => void
    fetchListMock.mockReturnValue(
      new Promise((res) => {
        resolveFetch = res
      })
    )

    const wrapper = mount(FilterBar, {
      props: { config: relationListConfig('incoming'), entityType: TAAK_TYPE, filters: {} },
    })
    await flushPromises()
    // Pre-load: no candidates yet → select mode (empty), not a crash.
    expect(targetSelectProps(wrapper).mode).toBe('select')

    resolveFetch({
      data: Array.from({ length: 11 }, (_, i) => persoon(`P${i}`, `Person ${i}`)),
      meta: META,
    })
    await flushPromises()
    // Post-load: exceeds threshold → typeahead.
    expect(targetSelectProps(wrapper).mode).toBe('typeahead')
  })

  it('a cancelled fetch for one control does not block a sibling control', async () => {
    seedRelationTypes({
      rel_a: { from: ['persoon'], to: ['taak'] },
      rel_b: { from: ['bedrijf'], to: ['taak'] },
    })
    // First control's fetch is cancelled; second resolves normally.
    fetchListMock
      .mockRejectedValueOnce({ name: 'AbortError' })
      .mockResolvedValueOnce({ data: [persoon('B-1', 'Acme')], meta: META })

    const config: ListConfig = {
      entity: 'taak',
      columns: [],
      filter_controls: [
        { relation: 'rel_a', label: 'A' },
        { relation: 'rel_b', label: 'B' },
      ],
    }
    const wrapper = mount(FilterBar, { props: { config, entityType: TAAK_TYPE, filters: {} } })
    await flushPromises()

    // Both controls render; the sibling (B) got its candidate loaded despite A's
    // cancellation. A cancel must NOT surface an error toast.
    const selects = wrapper.findAllComponents(EntityTargetSelect)
    expect(selects).toHaveLength(2)
    expect(uiErrorMock).not.toHaveBeenCalled()
  })

  it('a non-cancel fetch failure surfaces a toast and leaves the widget empty', async () => {
    seedRelationType('verantwoordelijk_voor', { from: ['persoon'], to: ['taak'] })
    fetchListMock.mockRejectedValue(new Error('boom'))

    const wrapper = mount(FilterBar, {
      props: { config: relationListConfig('incoming'), entityType: TAAK_TYPE, filters: {} },
    })
    await flushPromises()

    // Widget still renders (empty), and the failure is surfaced to the user.
    expect(wrapper.findComponent(EntityTargetSelect).exists()).toBe(true)
    expect(uiErrorMock).toHaveBeenCalledTimes(1)
  })
})

describe('FilterBar — property filters still render as before', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    fetchListMock.mockReset()
    uiErrorMock.mockReset()
    stubCandidates([])
  })

  it('an enum property renders a native <select> (no regression)', async () => {
    const config: ListConfig = {
      entity: 'taak',
      columns: [],
      filter_controls: [{ property: 'status' }],
    }
    const entityType: EntityType = {
      label: 'Taak',
      properties: { status: { type: 'enum', values: ['todo', 'doing', 'done'] } },
    }

    const wrapper = mount(FilterBar, { props: { config, entityType, filters: {} } })
    await flushPromises()

    // Native select from the property path, not the relation component.
    expect(wrapper.find('select').exists()).toBe(true)
    expect(wrapper.findComponent(EntityTargetSelect).exists()).toBe(false)
  })

  describe('text box', () => {
    const config: ListConfig = {
      entity: 'taak',
      columns: [],
      filter_controls: [{ property: 'owner' }, { property: 'points' }],
    }
    const entityType: EntityType = {
      label: 'Taak',
      properties: { owner: { type: 'string' }, points: { type: 'integer' } },
    }

    async function typeInto(index: number, value: string) {
      vi.useFakeTimers()
      try {
        const wrapper = mount(FilterBar, { props: { config, entityType, filters: {} } })
        await flushPromises()
        await wrapper.findAll('input')[index].setValue(value)
        vi.advanceTimersByTime(300)
        const emits = wrapper.emitted('filter') ?? []
        return emits[emits.length - 1]?.[0]
      } finally {
        vi.useRealTimers()
      }
    }

    it('on a string property matches case-insensitively on a substring', async () => {
      expect(await typeInto(0, 'anna')).toEqual({ owner: { value: 'anna', op: '~' } })
    })

    it('on a non-string property keeps exact equality', async () => {
      expect(await typeInto(1, '5')).toEqual({ points: { value: '5' } })
    })
  })
})
