<script setup lang="ts">
/**
 * The `@` completion menu.
 *
 * Purely presentational: it renders the menu state and emits `pick` and
 * `hover`. Search and selection live in `useMentionMenu`, and positioning is
 * done by Milkdown's `SlashProvider` on the wrapping element, so this
 * component sets no coordinates of its own.
 *
 * Focus stays in the editor. The mousedown handler preventDefaults so clicking
 * a row does not pull focus out of ProseMirror, which would close the menu
 * before the click resolves.
 */
import { computed, useId, type DeepReadonly } from 'vue'
import { entityDisplayTitle } from '@/utils/entityDisplay'
import type { Entity } from '@/types'
import type { MentionMenuState } from './useMentionMenu'

const props = defineProps<{
  /**
   * Readonly so the controller's own type binds directly: the menu is rendered
   * from state it must never mutate, and laundering the readonly away at the
   * call site is what a cast there was hiding.
   */
  state: DeepReadonly<MentionMenuState>
  /**
   * Which combined-list row is highlighted, resolved by the controller.
   *
   * Passed in rather than read off `state` because the highlight is stored as
   * an IDENTITY and resolved against the live rows; a position kept in state
   * could address a row that had changed underneath it.
   */
  highlightedIndex: number
}>()

const emit = defineEmits<{
  pick: [index: number]
  hover: [index: number]
}>()

function titleOf(item: DeepReadonly<Entity>): string {
  return entityDisplayTitle(item) || item.id
}

/**
 * The highlight addresses types and entities as one sequence, in the order the
 * plan chose (`typesFirst`). Each section's row index is offset by however many
 * rows of the other section precede it.
 */
const typeOffset = computed(() => (props.state.typesFirst ? 0 : props.state.items.length))
const entityOffset = computed(() => (props.state.typesFirst ? props.state.typeItems.length : 0))

/**
 * A per-instance id prefix, so two editors on one page cannot mint the same
 * option ids (`aria-activedescendant` resolves against the whole document).
 *
 * `useId()` rather than a random suffix: it is unique by construction per app
 * instance and stable across SSR hydration, where a random value would differ
 * between server and client. It also keeps `Math.random()` out of an id that
 * CodeQL reads as security-relevant (js/insecure-randomness) — the id is only
 * an ARIA pointer and authenticates nothing, but the deterministic API is the
 * better tool regardless, so there is nothing here worth suppressing.
 */
const uid = useId()

/** The DOM id of the row at `index` in the combined list. */
function optionId(index: number): string {
  return `${uid}-opt-${index}`
}

/**
 * The option `aria-activedescendant` points at.
 *
 * Without this a screen reader announces nothing as the highlight moves: the
 * rows are not focused (focus stays in the editor), so the only way to convey
 * the active row is to name it from the listbox.
 */
const activeOptionId = computed(() =>
  props.highlightedIndex >= 0 ? optionId(props.highlightedIndex) : undefined
)

/**
 * The note standing in for entity rows, or '' when rows show.
 *
 * A query that runs no search but matches a type gets no note: the type rows
 * are the whole answer.
 */
const entityNote = computed(() => {
  const s = props.state
  if (s.items.length > 0) return ''
  // One letter matching no type: nothing is searched yet, but an empty box
  // would look broken.
  if (!s.searches) return s.typeItems.length === 0 ? 'Type to search' : ''
  if (s.pending) return 'Searching…'
  if (s.errorMsg) return s.errorMsg
  return s.starting ? 'Type to search' : 'No matches'
})

/** Section order, as the plan decided; the DOM follows it so reading order does too. */
const sections = computed(() =>
  props.state.typesFirst ? (['types', 'entities'] as const) : (['entities', 'types'] as const)
)

/** Types sit in a compact row once search results lead (spec 3.6). */
const compactTypes = computed(() => !props.state.typesFirst && props.state.items.length > 0)
</script>

<template>
  <div
    class="mention-menu"
    role="listbox"
    aria-label="Entity reference suggestions"
    :aria-activedescendant="activeOptionId"
    @mousedown.prevent
  >
    <!-- The scope is document text (`@ticket:`) and is drawn as a chip there;
         this row repeats it so the list says what it is drawn from. The hint
         shows only while nothing follows the colon, where Backspace unscopes. -->
    <div v-if="props.state.scopeType" class="mention-menu-chip-row">
      <span class="mention-menu-chip">{{ props.state.scopeType }}</span>
      <span v-if="props.state.starting" class="mention-menu-chip-hint">Backspace to clear</span>
    </div>

    <template v-for="section in sections" :key="section">
      <div
        v-if="section === 'entities' && (entityNote || props.state.items.length > 0)"
        class="mention-menu-block"
      >
        <div
          v-if="props.state.typeItems.length > 0"
          :id="`${uid}-entities-label`"
          class="mention-menu-section"
        >
          Entities
        </div>
        <div
          v-if="entityNote"
          class="mention-menu-note"
          :class="{ 'mention-menu-error': props.state.errorMsg && !props.state.loading }"
        >
          {{ entityNote }}
        </div>
        <ul
          v-else-if="props.state.items.length > 0"
          class="mention-menu-list"
          role="group"
          :aria-labelledby="props.state.typeItems.length > 0 ? `${uid}-entities-label` : undefined"
          :aria-label="props.state.typeItems.length > 0 ? undefined : 'Entities'"
          data-section="entities"
        >
          <li
            v-for="(item, idx) in props.state.items"
            :id="optionId(entityOffset + idx)"
            :key="`entity:${item.id}`"
            class="mention-menu-item"
            :class="{ 'is-highlighted': entityOffset + idx === props.highlightedIndex }"
            role="option"
            :aria-selected="entityOffset + idx === props.highlightedIndex"
            @click="emit('pick', entityOffset + idx)"
            @mousemove="emit('hover', entityOffset + idx)"
          >
            <span class="mention-menu-title">{{ titleOf(item) }}</span>
            <!-- The ID is shown here and nowhere else. It disambiguates two
                 entities with the same title, and it is the label the editor
                 falls back to when a title never resolves. The rendered view
                 deliberately shows the title alone. -->
            <span class="mention-menu-id">{{ item.id }}</span>
          </li>
        </ul>
      </div>

      <!-- Types come from the already-loaded schema, so they render while a
           search is in flight rather than flickering behind the note. -->
      <div
        v-else-if="section === 'types' && props.state.typeItems.length > 0"
        class="mention-menu-block"
      >
        <div :id="`${uid}-types-label`" class="mention-menu-section">Types</div>
        <ul
          class="mention-menu-list"
          :class="{ 'is-compact': compactTypes }"
          role="group"
          :aria-labelledby="`${uid}-types-label`"
          data-section="types"
        >
          <li
            v-for="(name, idx) in props.state.typeItems"
            :id="optionId(typeOffset + idx)"
            :key="`type:${name}`"
            class="mention-menu-item"
            :class="{ 'is-highlighted': typeOffset + idx === props.highlightedIndex }"
            role="option"
            :aria-selected="typeOffset + idx === props.highlightedIndex"
            @click="emit('pick', typeOffset + idx)"
            @mousemove="emit('hover', typeOffset + idx)"
          >
            <span class="mention-menu-title">{{ name }}</span>
            <span v-if="!compactTypes" class="mention-menu-id">type</span>
          </li>
        </ul>
      </div>
    </template>
  </div>
</template>

<style scoped>
.mention-menu {
  /* Positioning and show/hide belong to the anchor element the slash provider
     writes coordinates onto (see MilkdownEditor.vue); this only styles the
     panel itself. */
  min-width: 260px;
  max-width: 420px;
  max-height: 280px;
  overflow-y: auto;
  border: 1px solid var(--border-color);
  border-radius: var(--radius-lg);
  background: var(--card-bg);
  box-shadow: var(--shadow-lg);
  font-size: var(--font-size-base);
}

.mention-menu-list {
  margin: 0;
  padding: var(--space-2xs) 0;
  list-style: none;
}

.mention-menu-item {
  display: flex;
  gap: var(--space-sm);
  align-items: baseline;
  justify-content: space-between;
  padding: var(--space-xs) var(--space-md);
  cursor: pointer;
}

.mention-menu-list.is-compact {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2xs);
  padding: var(--space-2xs) var(--space-md);
}

.mention-menu-list.is-compact .mention-menu-item {
  padding: 0 var(--space-xs);
  border-radius: var(--radius-sm);
  background: var(--hover-bg);
  font-family: monospace;
  font-size: var(--font-size-sm);
}

.mention-menu-item.is-highlighted {
  background: var(--hover-bg);
}

.mention-menu-title {
  overflow: hidden;
  color: var(--text-color);
  text-overflow: ellipsis;
  white-space: nowrap;
}

.mention-menu-id {
  flex-shrink: 0;
  color: var(--muted-text);
  font-size: var(--font-size-sm);
  font-family: monospace;
}

.mention-menu-note {
  padding: var(--space-sm) var(--space-md);
  color: var(--muted-text);
}

.mention-menu-section {
  padding: var(--space-2xs) var(--space-md);
  color: var(--muted-text);
  font-size: var(--font-size-sm);
  letter-spacing: 0.04em;
  text-transform: uppercase;
}

.mention-menu-block + .mention-menu-block {
  border-top: 1px solid var(--border-color);
  margin-top: var(--space-2xs);
  padding-top: var(--space-2xs);
}

.mention-menu-chip-row {
  display: flex;
  gap: var(--space-sm);
  align-items: baseline;
  padding: var(--space-xs) var(--space-md);
  border-bottom: 1px solid var(--border-color);
}

.mention-menu-chip {
  padding: 0 var(--space-xs);
  border-radius: var(--radius-sm);
  background: var(--hover-bg);
  color: var(--text-color);
  font-size: var(--font-size-sm);
  font-family: monospace;
}

.mention-menu-chip-hint {
  color: var(--muted-text);
  font-size: var(--font-size-sm);
}

.mention-menu-error {
  /* `--error-color` is the token this theme actually defines; the fallback
     literal that used to stand in here was a light-theme red that never
     adapted to dark mode. */
  color: var(--error-color);
}
</style>
