<script setup lang="ts">
/**
 * Quick-jump command palette.
 *
 * Cmd/Ctrl+K opens this from anywhere in the SPA. Type a few characters of an
 * entity title or ID, ArrowUp/ArrowDown to highlight, Enter to navigate. Esc
 * or backdrop click closes. Mirrors ConfirmModal's focus-restore lifecycle
 * and registers with the shared modal stack so list-shortcut handlers stand
 * down while it's open.
 *
 * Escape uses stopPropagation() — without it the global useKeyboardShortcuts
 * Escape branch fires next and may invoke router.back() on form pages.
 *
 * Tab and Shift+Tab are preventDefault'd on the dialog so focus cannot escape
 * behind the overlay (we don't have a generic useFocusTrap composable yet).
 */
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import { searchEntities } from '@/api'
import { entityDisplayTitle } from '@/utils/entityDisplay'
import { useSchemaStore } from '@/stores'
import { useModalStack } from '@/composables/modalStack'
import { isCancelledFetch } from '@/composables/usePageData'
import { useWorld } from '@/composables/useWorld'
import { entityDetailHref } from '@/utils/entityRoute'
import { shouldDeferToBrowser } from '@/utils/openIntent'
import type { Entity } from '@/types'

// Perceptually-instant on a fast connection; tune up if the API is slow.
const DEBOUNCE_MS = 150
// Minimum characters before we hit /_search. 1-letter queries return
// thousands of unrelated results in any non-trivial project.
const MIN_QUERY_LEN = 2
// Cap rendered options regardless of what the backend returns. Avoids OOMing
// the browser when the user types a common letter and the backend has no
// per_page on /_search yet.
const MAX_RESULTS = 50

const props = defineProps<{
  open: boolean
}>()

const emit = defineEmits<{
  close: []
}>()

const router = useRouter()
const schemaStore = useSchemaStore()

const query = ref('')
const results = ref<Entity[]>([])
const highlightedIndex = ref(0)
const loading = ref(false)
const errorMsg = ref('')

const inputRef = ref<HTMLInputElement | null>(null)
const previouslyFocused = ref<HTMLElement | null>(null)

// 8-char random suffix so multiple Teleport-mounted listboxes don't collide.
const listboxId = `cmdk-listbox-${Math.random().toString(36).slice(2, 10)}`
const optionId = (entity: Entity) => `cmdk-option-${entity.id}`

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
    // Replace results only on success — keep stale results visible until
    // the new ones arrive, to avoid flicker.
    results.value = resp.data.slice(0, MAX_RESULTS)
    errorMsg.value = ''
    highlightedIndex.value = 0
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
      // Sync flush means the v-if has already swapped — wait one tick for
      // the input ref to be populated, then focus it.
      void nextTick(() => inputRef.value?.focus())
    } else if (!isOpen && wasOpen) {
      cancelInflight()
      const prev = previouslyFocused.value
      previouslyFocused.value = null
      if (prev?.isConnected) {
        prev.focus()
      }
    }
  },
  // flush: 'sync' so a rapid open: true → false → true sequence sees both
  // transitions instead of the post-flush watcher coalescing to the latest.
  { immediate: true, flush: 'sync' }
)

// Cleans up after teardown when the component unmounts while still open
// (HMR, route-mounted variants, etc.). The watcher above only fires its
// cleanup on an open→closed transition; without this hook a pending timer
// or fetch would outlive the component.
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

// A modifier/middle click is the browser's to handle: it opens a tab from the
// anchor's own href, and the palette deliberately STAYS OPEN so the user can
// collect several entities into tabs without reopening it each time.
function selectEntity(entity: Entity, event?: MouseEvent): void {
  if (event && shouldDeferToBrowser(event)) return
  const href = entityDetailHref(entity)
  if (!href) return
  router.push(href)
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

// Keep the highlighted option in view as the user arrow-keys through long
// result lists. `block: 'nearest'` no-ops when the option is already visible,
// so the listbox doesn't jiggle on every move — only on actual overshoot.
// Called from keyboard handlers only; mouseenter sets highlightedIndex
// directly without scrolling so hovering doesn't fight with the user's mouse.
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
      // Block the global useKeyboardShortcuts Escape branch from firing
      // (would otherwise call router.back() on form routes).
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
      // Pin focus on the input. The palette has only one focusable element
      // today, so a real focus trap would be overkill; preventDefault keeps
      // focus from leaking behind the overlay. When more controls are added
      // (clear button, filter chips), swap this for a proper focus trap.
      e.preventDefault()
      return
  }
}

const showEmptyHint = computed(
  () =>
    query.value.trim().length < MIN_QUERY_LEN &&
    results.value.length === 0 &&
    !loading.value
)
const showNoMatches = computed(
  () =>
    query.value.trim().length >= MIN_QUERY_LEN &&
    !loading.value &&
    results.value.length === 0 &&
    !errorMsg.value
)
</script>

<template>
  <Teleport to="body">
    <div
      v-if="open"
      class="cmdk-overlay"
      role="dialog"
      aria-modal="true"
      aria-label="Quick jump"
      @click="handleOverlayClick"
      @keydown="handleKeydown"
    >
      <div class="cmdk-modal">
        <div class="cmdk-input-wrap">
          <input
            ref="inputRef"
            v-model="query"
            type="text"
            class="cmdk-input"
            placeholder="Type to search entities…"
            autocomplete="off"
            spellcheck="false"
            role="combobox"
            aria-autocomplete="list"
            :aria-controls="listboxId"
            :aria-expanded="results.length > 0"
            :aria-activedescendant="activeDescendant"
          />
          <span v-if="loading" class="cmdk-spinner" aria-hidden="true" />
        </div>

        <div v-if="showEmptyHint" class="cmdk-hint">Type to search entities</div>
        <div v-else-if="errorMsg" class="cmdk-hint cmdk-error">{{ errorMsg }}</div>
        <div v-else-if="showNoMatches" class="cmdk-hint">No matches</div>

        <ul
          v-if="results.length > 0"
          :id="listboxId"
          class="cmdk-results"
          role="listbox"
        >
          <li
            v-for="(entity, idx) in results"
            :id="optionId(entity)"
            :key="entity.id"
            class="cmdk-option"
            :class="{ 'cmdk-option-active': idx === highlightedIndex }"
            role="option"
            :aria-selected="idx === highlightedIndex"
            @mouseenter="highlightedIndex = idx"
          >
            <!-- The option's content renders in BOTH branches. Guarding only
                 the link would leave an entity with no resolvable route as an
                 empty, zero-height <li role="option">: invisible to the eye,
                 still counted by the arrow-key highlight, and nameless to a
                 screen reader. -->
            <RouterLink
              v-if="entityDetailHref(entity)"
              class="cmdk-option-link"
              :to="entityDetailHref(entity)"
              tabindex="-1"
              @click="selectEntity(entity, $event)"
            >
              <span class="cmdk-type">{{ entityTypeLabel(entity.type) }}</span>
              <span class="cmdk-title">{{ entityLabel(entity) }}</span>
              <span class="cmdk-id">{{ entity.id }}</span>
            </RouterLink>
            <span v-else class="cmdk-option-link">
              <span class="cmdk-type">{{ entityTypeLabel(entity.type) }}</span>
              <span class="cmdk-title">{{ entityLabel(entity) }}</span>
              <span class="cmdk-id">{{ entity.id }}</span>
            </span>
          </li>
        </ul>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.cmdk-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  align-items: flex-start;
  justify-content: center;
  padding-top: 12vh;
  z-index: 1000;
}

.cmdk-modal {
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

.cmdk-input-wrap {
  position: relative;
  border-bottom: 1px solid var(--border-color);
}

.cmdk-input {
  width: 100%;
  padding: 16px 44px 16px 18px;
  border: none;
  outline: none;
  background: transparent;
  color: var(--text-color);
  font-size: 16px;
  font-family: inherit;
}

.cmdk-spinner {
  position: absolute;
  top: 50%;
  right: 16px;
  transform: translateY(-50%);
  width: 16px;
  height: 16px;
  border: 2px solid var(--border-color);
  border-top-color: var(--accent-color);
  border-radius: 50%;
  animation: cmdk-spin 0.8s linear infinite;
}

@keyframes cmdk-spin {
  to {
    transform: translateY(-50%) rotate(360deg);
  }
}

.cmdk-hint {
  padding: 24px 18px;
  color: var(--muted-text);
  font-size: 14px;
  text-align: center;
}

.cmdk-error {
  color: var(--error-color);
}

.cmdk-results {
  list-style: none;
  margin: 0;
  padding: 4px 0;
  overflow-y: auto;
  max-height: calc(70vh - 60px);
}

.cmdk-option {
  cursor: pointer;
  font-size: 14px;
}

/* Padding lives on the link, not the <li>, so the whole option row is inside
   the anchor's hit area (and its href) rather than only the text. */
.cmdk-option-link {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 18px;
  color: inherit;
  text-decoration: none;
}

.cmdk-option-active {
  background: var(--hover-bg);
}

.cmdk-type {
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

.cmdk-option-active .cmdk-type {
  background: var(--card-bg);
}

.cmdk-title {
  flex: 1;
  color: var(--text-color);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.cmdk-id {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 12px;
  color: var(--muted-text);
  flex-shrink: 0;
}

/* Reduced motion. This is a SCOPED style, so styles/pending.css cannot
   reach .cmdk-spinner — a scoped selector carries a [data-v-*] attribute and
   outranks an unscoped rule. The suppression has to live beside the
   declaration. */
@media (prefers-reduced-motion: reduce) {
  .cmdk-spinner {
    animation: none;
  }
}
</style>
