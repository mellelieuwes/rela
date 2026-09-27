<script setup lang="ts">
import { ref, computed, watch, onBeforeUnmount, nextTick, useTemplateRef } from 'vue'
import { useUIStore } from '@/stores'
import { useConfirm } from '@/composables/useConfirm'
import {
  addComment,
  updateComment,
  deleteComment,
  type Comment,
  type CommentAnchor,
} from '@/api/comments'
import { getErrorMessage } from '@/api/errors'

/**
 * A comment affordance anchored to one field (TKT-FIO205).
 *
 * Renders a small indicator beside the property label and, on click, a popover
 * holding that anchor's thread plus a composer.
 *
 * # Why the indicator sits by the LABEL, not the value
 *
 * A field is a column (label above value) on a 12-column grid, so a field may
 * be a third of the row wide. Trailing the value puts the badge at a position
 * that moves with the value's length, which on a shared row reads as belonging
 * to the neighbouring field. The label is a fixed anchor point at any span.
 *
 * # Data flows in, mutations flow out
 *
 * The parent owns the comment list (one fetch per entity, not one per field).
 * This component receives its slice and emits `changed` so the parent refetches
 * — a per-field fetch would be N requests for N fields.
 */
const props = defineProps<{
  entityType: string
  entityId: string
  anchor: CommentAnchor
  /** This anchor's comments, already filtered by the parent. */
  comments: Comment[]
  /**
   * Open the popover to the right instead of the left. The parent decides,
   * because only it knows the field's position in the row: a popover is wider
   * than a 4-column field, so on the last column a left-aligned one would
   * overflow the card.
   */
  flip?: boolean
}>()

const emit = defineEmits<{ changed: [] }>()

const uiStore = useUIStore()
const { confirm } = useConfirm()

const open = ref(false)
const body = ref('')
const submitting = ref(false)
const editingId = ref<string | null>(null)
const editBody = ref('')
const rootRef = useTemplateRef<HTMLElement>('root')
const composerRef = useTemplateRef<HTMLTextAreaElement>('composer')

const total = computed(() => props.comments.length)
const openCount = computed(() => props.comments.filter((c) => !c.resolved).length)
const allResolved = computed(() => total.value > 0 && openCount.value === 0)
const detached = computed(() => props.comments.some((c) => c.detached))

/** Unresolved first: a settled remark should not bury an active one. */
const ordered = computed(() =>
  [...props.comments].sort((a, b) => Number(a.resolved) - Number(b.resolved))
)

const title = computed(() => {
  if (total.value === 0) return `Comment on ${props.anchor.ref}`
  const noun = total.value === 1 ? 'comment' : 'comments'
  return `${total.value} ${noun} on ${props.anchor.ref}`
})

async function toggle() {
  open.value = !open.value
  if (open.value) {
    await nextTick()
    composerRef.value?.focus()
  }
}

function close() {
  open.value = false
  editingId.value = null
  editBody.value = ''
}

async function submit() {
  const text = body.value.trim()
  if (!text || submitting.value) return
  submitting.value = true
  try {
    await addComment(props.entityType, props.entityId, {
      anchor: { kind: props.anchor.kind, ref: props.anchor.ref },
      body: text,
    })
    body.value = ''
    emit('changed')
  } catch (err) {
    uiStore.error(getErrorMessage(err))
  } finally {
    submitting.value = false
  }
}

function startEdit(c: Comment) {
  editingId.value = c.id
  editBody.value = c.body
}

async function saveEdit(c: Comment) {
  const text = editBody.value.trim()
  if (!text) return
  try {
    await updateComment(props.entityType, props.entityId, c.id, { body: text })
    editingId.value = null
    emit('changed')
  } catch (err) {
    uiStore.error(getErrorMessage(err))
  }
}

async function toggleResolved(c: Comment) {
  try {
    await updateComment(props.entityType, props.entityId, c.id, { resolved: !c.resolved })
    emit('changed')
  } catch (err) {
    uiStore.error(getErrorMessage(err))
  }
}

async function remove(c: Comment) {
  // Branch on the boolean: useConfirm resolves false if the shell unmounts
  // while the dialog is open, and a DELETE must not fire on that path.
  const ok = await confirm({
    title: 'Delete comment',
    message: 'Delete this comment? This cannot be undone.',
    confirmLabel: 'Delete',
    danger: true,
  })
  if (!ok) return
  try {
    await deleteComment(props.entityType, props.entityId, c.id)
    emit('changed')
  } catch (err) {
    uiStore.error(getErrorMessage(err))
  }
}

function onDocClick(e: MouseEvent) {
  if (!rootRef.value?.contains(e.target as Node)) close()
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') close()
}

// Listeners only exist while the popover is open — a document-level handler per
// field would otherwise be one listener per property on every entity page.
watch(open, (isOpen) => {
  if (isOpen) {
    document.addEventListener('click', onDocClick)
    document.addEventListener('keydown', onKeydown)
  } else {
    document.removeEventListener('click', onDocClick)
    document.removeEventListener('keydown', onKeydown)
  }
})

onBeforeUnmount(() => {
  document.removeEventListener('click', onDocClick)
  document.removeEventListener('keydown', onKeydown)
})

function formatDate(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}
</script>

<template>
  <span ref="root" class="comment-indicator">
    <button
      type="button"
      class="ci-btn"
      :class="{
        'ci-btn--has': total > 0 && !allResolved,
        'ci-btn--done': allResolved,
        'ci-btn--empty': total === 0,
        'ci-btn--detached': detached,
      }"
      :title="title"
      :aria-label="title"
      :aria-expanded="open"
      @click.stop="toggle"
    >
      <svg
        v-if="allResolved"
        viewBox="0 0 16 16"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
      >
        <path d="M3 8.5 6.5 12 13 4.5" stroke-linecap="round" stroke-linejoin="round" />
      </svg>
      <svg v-else-if="total > 0" viewBox="0 0 16 16" fill="currentColor">
        <path d="M8 1a7 7 0 0 0-6.1 10.4L1 15l3.8-.9A7 7 0 1 0 8 1Z" />
      </svg>
      <svg v-else viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6">
        <path d="M8 4.5v7M4.5 8h7" stroke-linecap="round" />
      </svg>
      <span v-if="total > 0" class="ci-count">{{ total }}</span>
    </button>

    <div v-if="open" class="ci-pop" :class="{ 'ci-pop--flip': flip }" @click.stop>
      <header class="ci-pop-head">
        <span
          >Comments on <code>{{ anchor.ref }}</code></span
        >
        <button type="button" class="ci-x" aria-label="Close" @click="close">✕</button>
      </header>

      <ul v-if="ordered.length > 0" class="ci-list">
        <li
          v-for="c in ordered"
          :key="c.id"
          class="ci-cmt"
          :class="{ 'ci-cmt--resolved': c.resolved }"
        >
          <div class="ci-meta">
            <b>{{ c.author }}</b>
            <span>{{ formatDate(c.created_at) }}</span>
            <span
              v-if="c.detached"
              class="ci-detached"
              title="The property this comment points at no longer exists"
              >detached</span
            >
          </div>

          <template v-if="editingId === c.id">
            <textarea v-model="editBody" class="ci-input" rows="3" />
            <div class="ci-acts">
              <button class="ci-mini ci-mini--primary" @click="saveEdit(c)">Save</button>
              <button class="ci-mini" @click="editingId = null">Cancel</button>
            </div>
          </template>

          <template v-else>
            <p class="ci-body">{{ c.body }}</p>
            <div class="ci-acts">
              <button v-if="c.editable" class="ci-mini" @click="toggleResolved(c)">
                {{ c.resolved ? 'Reopen' : 'Resolve' }}
              </button>
              <button v-if="c.editable" class="ci-mini" @click="startEdit(c)">Edit</button>
              <button v-if="c.deletable" class="ci-mini ci-mini--danger" @click="remove(c)">
                Delete
              </button>
            </div>
          </template>
        </li>
      </ul>

      <!-- Always offered: whether this user may post is the server's call, and
           a hidden box would misreport a permission we were never told. -->
      <form class="ci-composer" @submit.prevent="submit">
        <textarea
          ref="composer"
          v-model="body"
          class="ci-input"
          rows="3"
          :placeholder="total > 0 ? 'Reply…' : 'Add a comment…'"
          aria-label="Comment body"
          @keydown.meta.enter="submit"
          @keydown.ctrl.enter="submit"
        />
        <div class="ci-composer-row">
          <span class="ci-hint">⌘↵ to post</span>
          <button
            type="submit"
            class="ci-mini ci-mini--primary"
            :disabled="submitting || !body.trim()"
          >
            {{ submitting ? 'Adding…' : 'Comment' }}
          </button>
        </div>
      </form>
    </div>
  </span>
</template>

<style scoped>
.comment-indicator {
  position: relative;
  display: inline-flex;
  vertical-align: middle;
}

.ci-btn {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  height: 19px;
  padding: 0 6px;
  border: 1px solid transparent;
  border-radius: 10px;
  background: none;
  cursor: pointer;
  font: 600 var(--font-size-sm) / 1 inherit;
  color: var(--muted-text);
}
.ci-btn svg {
  width: 11px;
  height: 11px;
}

.ci-btn--has {
  background: color-mix(in srgb, var(--accent-color) 12%, transparent);
  border-color: color-mix(in srgb, var(--accent-color) 30%, transparent);
  color: var(--accent-color);
}
.ci-btn--done {
  background: color-mix(in srgb, var(--success-color) 14%, transparent);
  border-color: color-mix(in srgb, var(--success-color) 32%, transparent);
  color: var(--success-color);
}
.ci-btn--detached {
  background: color-mix(in srgb, var(--warning-color) 22%, transparent);
  border-color: color-mix(in srgb, var(--warning-color) 45%, transparent);
  color: var(--text-color);
}

/* Empty fields stay quiet until the row is hovered or the control is focused.
 * Focus-visible is not optional here: an opacity-0 control that never reveals
 * on keyboard focus is unreachable without a mouse. */
.ci-btn--empty {
  opacity: 0;
  transition: opacity 0.12s;
}
.property-item:hover .ci-btn--empty,
.ci-btn--empty:hover,
.ci-btn--empty:focus-visible,
.ci-btn--empty[aria-expanded='true'] {
  opacity: 0.55;
}
.ci-btn--empty:hover,
.ci-btn--empty:focus-visible {
  opacity: 1;
}

.ci-btn:focus-visible {
  outline: none;
  box-shadow:
    0 0 0 2px var(--focus-ring-gap),
    0 0 0 4px var(--focus-ring);
}

/* ── Popover ─────────────────────────────────────────────────── */
.ci-pop {
  position: absolute;
  z-index: 30;
  top: calc(100% + 6px);
  left: 0;
  width: 340px;
  background: var(--card-bg);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-lg, 8px);
  box-shadow: var(--shadow-lg, 0 10px 30px rgba(0, 0, 0, 0.16));
  text-align: left;
  cursor: default;
}
/* Flipped, the popover extends LEFTWARD from the indicator.
 *
 * `right: 0` here aligns to the indicator's right edge, not the field's — the
 * indicator is the positioning parent and is only ~30px wide. That is what we
 * want: the popover hangs left from the badge the user clicked, which keeps it
 * inside the content area near the right edge of the grid.
 *
 * It is NOT a substitute for the field-position check the parent does: on a
 * full-width field the badge sits near the LEFT of the page, so flipping there
 * would push the popover under the sidebar. Hence `flip` is only ever set for a
 * field that is genuinely rightmost on a SHARED row. */
.ci-pop--flip {
  left: auto;
  right: 0;
}

.ci-pop-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 12px;
  border-bottom: 1px solid var(--border-color);
  font-size: var(--font-size-sm);
  color: var(--muted-text);
}
.ci-pop-head code {
  color: var(--text-color);
}
.ci-x {
  border: 0;
  background: none;
  cursor: pointer;
  color: var(--muted-text);
  font-size: var(--font-size-sm);
}

.ci-list {
  list-style: none;
  margin: 0;
  padding: 0;
  max-height: 320px;
  overflow-y: auto;
}
.ci-cmt {
  padding: 10px 12px;
  border-bottom: 1px solid var(--border-color);
}
.ci-cmt:last-child {
  border-bottom: 0;
}
.ci-cmt--resolved {
  opacity: 0.6;
}

.ci-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  font-size: var(--font-size-sm);
  color: var(--muted-text);
  margin-bottom: 3px;
}
.ci-meta b {
  color: var(--text-color);
}
.ci-detached {
  padding: 1px 6px;
  border-radius: 4px;
  background: var(--warning-color);
  color: var(--text-color);
}

.ci-body {
  margin: 0 0 6px;
  font-size: var(--font-size-base);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.ci-acts {
  display: flex;
  gap: 5px;
}
.ci-mini {
  font: var(--font-size-sm) / 1.4 inherit;
  padding: 2px 8px;
  border-radius: var(--radius-sm, 4px);
  border: 1px solid var(--border-color);
  background: var(--bg-color);
  color: var(--text-color);
  cursor: pointer;
}
.ci-mini--primary {
  background: var(--accent-color);
  border-color: var(--accent-color);
  color: #fff;
}
.ci-mini--danger {
  color: var(--error-color);
}
.ci-mini:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}
.ci-mini:focus-visible {
  outline: none;
  box-shadow:
    0 0 0 2px var(--focus-ring-gap),
    0 0 0 4px var(--focus-ring);
}

.ci-composer {
  padding: 10px 12px;
  border-top: 1px solid var(--border-color);
  background: var(--bg-color);
}
.ci-input {
  width: 100%;
  padding: 6px 8px;
  resize: vertical;
  border: 1px solid var(--border-color);
  border-radius: var(--radius-sm, 5px);
  background: var(--input-bg);
  color: var(--text-color);
  font: inherit;
  font-size: var(--font-size-base);
}
.ci-input:focus {
  outline: none;
  border-color: var(--accent-color);
  box-shadow:
    0 0 0 2px var(--focus-ring-gap),
    0 0 0 4px var(--focus-ring);
}
.ci-composer-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-top: 6px;
}
.ci-hint {
  font-size: var(--font-size-sm);
  color: var(--muted-text);
}

/* On a narrow viewport the popover cannot sit beside a field at all — the
 * grid has already collapsed to one column, so it spans the row instead. */
@media (max-width: 640px) {
  .ci-pop,
  .ci-pop--flip {
    left: 0;
    right: 0;
    width: auto;
  }
}
</style>
