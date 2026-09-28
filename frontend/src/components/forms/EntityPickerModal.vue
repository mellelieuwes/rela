<script setup lang="ts">
/**
 * Modal entity picker used by the markdown editor's "Insert entity
 * reference" toolbar button (TKT-I5NO). Searches via `/_search`, emits
 * the selected entity's ID. The parent decides what to do with the ID
 * (the markdown editor wraps it in backticks and inserts at the cursor).
 *
 * Modeled on CommandPaletteModal but with a narrower action contract:
 * `select(id)` instead of `router.push`. Kept as a sibling rather than
 * a generalization of the palette to avoid regression risk in the
 * Cmd+K flow — a future ticket can DRY them when a third consumer
 * appears.
 *
 * z-index of the overlay is 10000, one above EasyMDE's fullscreen
 * z-index of 9999, so the picker remains on top in fullscreen mode
 * (RR-WMG2).
 */
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { searchEntities } from '@/api'
import { entityDisplayTitle } from '@/utils/entityDisplay'
import { useSchemaStore } from '@/stores'
import { useModalStack } from '@/composables/modalStack'
import { isCancelledFetch } from '@/composables/usePageData'
import { useWorld } from '@/composables/useWorld'
import type { Entity } from '@/types'

const DEBOUNCE_MS = 150
const MIN_QUERY_LEN = 2
const MAX_RESULTS = 50

const props = defineProps<{
  open: boolean
}>()

const emit = defineEmits<{
  close: []
  select: [id: string]
}>()

const schemaStore = useSchemaStore()

const query = ref('')
const results = ref<Entity[]>([])
const highlightedIndex = ref(0)
const loading = ref(false)
const errorMsg = ref('')

const inputRef = ref<HTMLInputElement | null>(null)
const previouslyFocused = ref<HTMLElement | null>(null)

// Random suffix so multiple Teleport-mounted listboxes don't collide on
// the document. Same idea as CommandPaletteModal:57.
const listboxId = `entity-picker-listbox-${Math.random().toString(36).slice(2, 10)}`
const optionId = (entity: Entity) => `entity-picker-option-${entity.id}`

const activeDescendant = computed(() => {
  const r = results.value[highlightedIndex.value]
  return r ? optionId(r) : undefined
})

useModalStack(computed(() => props.open))

let abort: AbortController | null = null
let debounceTimer: ReturnType<typeof setTimeout> | null = null

function cancelInflight(): void {
  if (debounceTimer) {
    clearTimeout(debounceTimer)
    debounceTimer = null
  }
  abort?.abort()
  abort = null
}

watch(query, (q) => {
  cancelInflight()
  const trimmed = q.trim()
  if (trimmed.length < MIN_QUERY_LEN) {
    results.value = []
    loading.value = false
    errorMsg.value = ''
    highlightedIndex.value = 0
    return
  }
  debounceTimer = setTimeout(() => {
    void runSearch(trimmed)
  }, DEBOUNCE_MS)
})

const { worldParam } = useWorld()

async function runSearch(q: string): Promise<void> {
  abort = new AbortController()
  loading.value = true
  try {
    const resp = await searchEntities(q, undefined, abort.signal, worldParam.value)
    results.value = resp.data.slice(0, MAX_RESULTS)
    errorMsg.value = ''
    highlightedIndex.value = 0
    // Reset scroll to the new top result. Without this, if the previous
    // search left the list scrolled down, the freshly-highlighted index
    // 0 lives offscreen — aria-activedescendant points at an option the
    // screen reader cannot follow (RR-AYFK).
    scrollHighlightedIntoView()
  } catch (err: unknown) {
    if (isCancelledFetch(err)) return
    errorMsg.value = 'Search failed'
  } finally {
    loading.value = false
  }
}

watch(
  () => props.open,
  (isOpen, wasOpen) => {
    if (isOpen && !wasOpen) {
      query.value = ''
      results.value = []
      highlightedIndex.value = 0
      errorMsg.value = ''
      loading.value = false
      previouslyFocused.value = (document.activeElement as HTMLElement) ?? null
      void nextTick(() => inputRef.value?.focus())
    } else if (!isOpen && wasOpen) {
      // Abort any in-flight search BEFORE we drop references, so a late
      // response can't land in a closed modal (RR-S7I8).
      cancelInflight()
      const prev = previouslyFocused.value
      previouslyFocused.value = null
      if (prev?.isConnected) {
        prev.focus()
      }
    }
  },
  { immediate: true, flush: 'sync' },
)

onBeforeUnmount(() => {
  cancelInflight()
  previouslyFocused.value = null
})

function entityLabel(entity: Entity): string {
  return entityDisplayTitle(entity)
}

function entityTypeLabel(type: string): string {
  if (!type) return 'Unknown'
  return schemaStore.entityTypes.get(type)?.label || type
}

function selectEntity(entity: Entity): void {
  if (!entity?.id) return
  emit('select', entity.id)
  emit('close')
}

function handleOverlayClick(e: MouseEvent): void {
  if (e.target === e.currentTarget) {
    emit('close')
  }
}

function moveHighlight(delta: number): void {
  const len = results.value.length
  if (len === 0) {
    highlightedIndex.value = 0
    return
  }
  highlightedIndex.value = (highlightedIndex.value + delta + len) % len
  scrollHighlightedIntoView()
}

function scrollHighlightedIntoView(): void {
  void nextTick(() => {
    const entity = results.value[highlightedIndex.value]
    if (!entity) return
    document.getElementById(optionId(entity))?.scrollIntoView({ block: 'nearest' })
  })
}

function handleKeydown(e: KeyboardEvent): void {
  switch (e.key) {
    case 'Escape':
      e.stopPropagation()
      emit('close')
      return
    case 'Enter': {
      const selected = results.value[highlightedIndex.value]
      if (selected) {
        e.preventDefault()
        selectEntity(selected)
      }
      return
    }
    case 'ArrowDown':
      e.preventDefault()
      moveHighlight(1)
      return
    case 'ArrowUp':
      e.preventDefault()
      moveHighlight(-1)
      return
    case 'Tab':
      e.preventDefault()
      return
  }
}

const showEmptyHint = computed(
  () =>
    query.value.trim().length < MIN_QUERY_LEN &&
    results.value.length === 0 &&
    !loading.value,
)
const showNoMatches = computed(
  () =>
    query.value.trim().length >= MIN_QUERY_LEN &&
    !loading.value &&
    results.value.length === 0 &&
    !errorMsg.value,
)
</script>

<template>
  <Teleport to="body">
    <div
      v-if="open"
      class="entity-picker-overlay"
      role="dialog"
      aria-modal="true"
      aria-label="Insert entity reference"
      @click="handleOverlayClick"
      @keydown="handleKeydown"
    >
      <div class="entity-picker-modal">
        <div class="entity-picker-input-wrap">
          <input
            ref="inputRef"
            v-model="query"
            type="text"
            class="entity-picker-input"
            placeholder="Type to search entities…"
            autocomplete="off"
            spellcheck="false"
            role="combobox"
            aria-autocomplete="list"
            :aria-controls="listboxId"
            :aria-expanded="results.length > 0"
            :aria-activedescendant="activeDescendant"
          />
          <span v-if="loading" class="entity-picker-spinner" aria-hidden="true" />
        </div>

        <div v-if="showEmptyHint" class="entity-picker-hint">Type to search entities</div>
        <div v-else-if="errorMsg" class="entity-picker-hint entity-picker-error">{{ errorMsg }}</div>
        <div v-else-if="showNoMatches" class="entity-picker-hint">No matches</div>

        <ul
          v-if="results.length > 0"
          :id="listboxId"
          class="entity-picker-results"
          role="listbox"
        >
          <li
            v-for="(entity, idx) in results"
            :id="optionId(entity)"
            :key="entity.id"
            class="entity-picker-option"
            :class="{ 'entity-picker-option-active': idx === highlightedIndex }"
            role="option"
            :aria-selected="idx === highlightedIndex"
            @click="selectEntity(entity)"
            @mouseenter="highlightedIndex = idx"
          >
            <span class="entity-picker-type">{{ entityTypeLabel(entity.type) }}</span>
            <span class="entity-picker-title">{{ entityLabel(entity) }}</span>
            <span class="entity-picker-id">{{ entity.id }}</span>
          </li>
        </ul>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
/* z-index: 10000 sits one above EasyMDE's fullscreen z-index of 9999
   (see MarkdownEditor.vue fullscreen rules) so the picker remains on
   top when the editor is in fullscreen mode (RR-WMG2). */
.entity-picker-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  align-items: flex-start;
  justify-content: center;
  padding-top: 12vh;
  z-index: 10000;
}

.entity-picker-modal {
  background: var(--card-bg);
  border-radius: 12px;
  box-shadow: 0 20px 60px rgba(0, 0, 0, 0.25);
  width: 90%;
  max-width: 640px;
  max-height: 70vh;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

.entity-picker-input-wrap {
  position: relative;
  border-bottom: 1px solid var(--border-color);
}

.entity-picker-input {
  width: 100%;
  padding: 16px 44px 16px 18px;
  border: none;
  outline: none;
  background: transparent;
  color: var(--text-color);
  font-size: 16px;
  font-family: inherit;
}

.entity-picker-spinner {
  position: absolute;
  top: 50%;
  right: 16px;
  transform: translateY(-50%);
  width: 16px;
  height: 16px;
  border: 2px solid var(--border-color);
  border-top-color: var(--accent-color);
  border-radius: 50%;
  animation: entity-picker-spin 0.8s linear infinite;
}

@keyframes entity-picker-spin {
  to {
    transform: translateY(-50%) rotate(360deg);
  }
}

.entity-picker-hint {
  padding: 24px 18px;
  color: var(--muted-text);
  font-size: 14px;
  text-align: center;
}

.entity-picker-error {
  color: var(--error-color);
}

.entity-picker-results {
  list-style: none;
  margin: 0;
  padding: 4px 0;
  overflow-y: auto;
  max-height: calc(70vh - 60px);
}

.entity-picker-option {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 18px;
  cursor: pointer;
  font-size: 14px;
}

.entity-picker-option-active {
  background: var(--hover-bg);
}

.entity-picker-type {
  font-size: 11px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  padding: 2px 8px;
  background: var(--hover-bg);
  color: var(--muted-text);
  border-radius: 4px;
  font-weight: 500;
  flex-shrink: 0;
}

.entity-picker-option-active .entity-picker-type {
  background: var(--card-bg);
}

.entity-picker-title {
  flex: 1;
  color: var(--text-color);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.entity-picker-id {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 12px;
  color: var(--muted-text);
  flex-shrink: 0;
}

/* Reduced motion. This is a SCOPED style, so styles/pending.css cannot
   reach .entity-picker-spinner — a scoped selector carries a [data-v-*] attribute and
   outranks an unscoped rule. The suppression has to live beside the
   declaration. */
@media (prefers-reduced-motion: reduce) {
  .entity-picker-spinner {
    animation: none;
  }
}
</style>
