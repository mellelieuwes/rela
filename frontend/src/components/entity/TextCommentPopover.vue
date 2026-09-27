<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount, useTemplateRef } from 'vue'
import { useUIStore } from '@/stores'
import { useConfirm } from '@/composables/useConfirm'
import { addComment, updateComment, deleteComment, type Comment } from '@/api/comments'
import { getErrorMessage } from '@/api/errors'
import SuggestionDiff from './SuggestionDiff.vue'

/**
 * The thread for a text-anchored comment, opened by clicking its highlight
 * (TKT-FIO205 stage 2).
 *
 * Separate from CommentIndicator because a highlight has no component of its
 * own: the marks are inserted into the body's `v-html` output, so the parent
 * owns both the click state and the placement, and passes them here.
 */
const props = defineProps<{
  entityType: string
  entityId: string
  /** The clicked highlight's comments. */
  comments: Comment[]
  /** Placement in coordinates relative to the body element. */
  position: { top: number; left: number }
  /**
   * Whether this user may apply suggestions right now: the entity's
   * `_actions.update`, and no other body write in flight. Combined with each
   * comment's own `acceptable` hint.
   */
  canAccept?: boolean
}>()

// `accept` goes to the parent rather than being handled here: the parent owns
// the body's pending writes, which must settle before the accept is sent.
const emit = defineEmits<{ changed: []; close: []; accept: [comment: Comment] }>()

const uiStore = useUIStore()
const { confirm } = useConfirm()

const editingId = ref<string | null>(null)
const editBody = ref('')
const rootRef = useTemplateRef<HTMLElement>('root')

const replyBody = ref('')
const replying = ref(false)

/**
 * Posts a reply against the SAME anchor as the comment being replied to.
 *
 * Stage 1 has no threading — replies are separate comments that happen to share
 * an anchor, which is why this re-sends the original's quote and context rather
 * than a parent id. That keeps the reply pinned to the same text even after the
 * body is edited, since it re-resolves independently.
 */
async function submitReply() {
  const text = replyBody.value.trim()
  const first = props.comments[0]
  if (!text || !first || replying.value) return

  replying.value = true
  try {
    await addComment(props.entityType, props.entityId, {
      anchor: {
        kind: 'text',
        ref: '',
        quote: first.anchor.quote,
        // The stored quote is SOURCE text, so no rendered-text context applies;
        // it is already unique enough to have resolved once.
      },
      body: text,
    })
    replyBody.value = ''
    emit('changed')
  } catch (err) {
    uiStore.error(getErrorMessage(err))
  } finally {
    replying.value = false
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
    emit('close')
  } catch (err) {
    uiStore.error(getErrorMessage(err))
  }
}

function onDocClick(e: MouseEvent) {
  const target = e.target as HTMLElement | null
  // A click on a highlight is the parent's to interpret — it may be opening a
  // different thread, and closing here first would fight that.
  if (target?.closest('mark[data-comment-id]')) return
  if (!rootRef.value?.contains(target)) emit('close')
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') emit('close')
}

onMounted(() => {
  document.addEventListener('click', onDocClick)
  document.addEventListener('keydown', onKeydown)
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
  <div
    ref="root"
    class="tcp"
    :style="{ top: `${position.top}px`, left: `${position.left}px` }"
    @click.stop
  >
    <header class="tcp-head">
      <span class="tcp-title">Comment on selection</span>
      <button type="button" class="tcp-x" aria-label="Close" @click="emit('close')">✕</button>
    </header>

    <ul class="tcp-list">
      <li
        v-for="c in comments"
        :key="c.id"
        class="tcp-cmt"
        :class="{ 'tcp-cmt--resolved': c.resolved }"
      >
        <SuggestionDiff
          v-if="c.anchor.quote && c.anchor.replacement != null"
          :quote="c.anchor.quote"
          :replacement="c.anchor.replacement"
        />
        <blockquote v-else-if="c.anchor.quote" class="tcp-quote">{{ c.anchor.quote }}</blockquote>
        <div class="tcp-meta">
          <b>{{ c.author }}</b>
          <span>{{ formatDate(c.created_at) }}</span>
          <span v-if="c.anchor.uncertain" class="tcp-flag" title="The text may have moved">
            may have moved
          </span>
          <span
            v-if="c.detached"
            class="tcp-flag tcp-flag--detached"
            title="The quoted text is gone"
          >
            detached
          </span>
        </div>

        <template v-if="editingId === c.id">
          <textarea v-model="editBody" class="tcp-input" rows="3" />
          <div class="tcp-acts">
            <button class="tcp-mini tcp-mini--primary" @click="saveEdit(c)">Save</button>
            <button class="tcp-mini" @click="editingId = null">Cancel</button>
          </div>
        </template>

        <template v-else>
          <p class="tcp-body">{{ c.body }}</p>
          <div class="tcp-acts">
            <button
              v-if="c.acceptable && canAccept"
              class="tcp-mini tcp-mini--primary"
              @click="emit('accept', c)"
            >
              Accept
            </button>
            <button v-if="c.editable" class="tcp-mini" @click="toggleResolved(c)">
              {{ c.resolved ? 'Reopen' : 'Resolve' }}
            </button>
            <button v-if="c.editable" class="tcp-mini" @click="startEdit(c)">Edit</button>
            <button v-if="c.deletable" class="tcp-mini tcp-mini--danger" @click="remove(c)">
              Delete
            </button>
          </div>
        </template>
      </li>
    </ul>

    <!-- Replies are separate comments sharing this anchor: stage 1 has no
         threading, so they re-resolve independently against the same text. -->
    <form class="tcp-reply" @submit.prevent="submitReply">
      <textarea
        v-model="replyBody"
        class="tcp-input"
        rows="2"
        placeholder="Reply…"
        aria-label="Reply"
        @keydown.meta.enter="submitReply"
        @keydown.ctrl.enter="submitReply"
      />
      <div class="tcp-reply-row">
        <span class="tcp-hint">⌘↵ to post</span>
        <button
          type="submit"
          class="tcp-mini tcp-mini--primary"
          :disabled="replying || !replyBody.trim()"
        >
          {{ replying ? 'Posting…' : 'Reply' }}
        </button>
      </div>
    </form>
  </div>
</template>

<style scoped>
.tcp {
  position: absolute;
  z-index: 26;
  width: 340px;
  background: var(--card-bg);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-lg, 8px);
  box-shadow: var(--shadow-lg, 0 10px 30px rgb(0 0 0 / 16%));
  text-align: left;
}

.tcp-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 12px;
  border-bottom: 1px solid var(--border-color);
}
.tcp-title {
  font-size: var(--font-size-sm);
  color: var(--muted-text);
}
.tcp-x {
  border: 0;
  background: none;
  cursor: pointer;
  color: var(--muted-text);
  font-size: var(--font-size-sm);
}

.tcp-list {
  list-style: none;
  margin: 0;
  padding: 0;
  max-height: 340px;
  overflow-y: auto;
}
.tcp-cmt {
  padding: 10px 12px;
  border-bottom: 1px solid var(--border-color);
}
.tcp-cmt:last-child {
  border-bottom: 0;
}
.tcp-cmt--resolved {
  opacity: 0.6;
}

/* The anchored text, so the thread states what it is about even when the
 * highlight is scrolled out of view or has detached. */
.tcp-quote {
  margin: 0 0 6px;
  padding: 3px 8px;
  border-left: 3px solid var(--accent-color);
  background: var(--bg-color);
  color: var(--muted-text);
  font-size: var(--font-size-sm);
  font-style: italic;
  max-height: 3.4em;
  overflow: hidden;
}

.tcp-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  font-size: var(--font-size-sm);
  color: var(--muted-text);
  margin-bottom: 3px;
}
.tcp-meta b {
  color: var(--text-color);
}
.tcp-flag {
  padding: 1px 6px;
  border-radius: 4px;
  background: var(--warning-color);
  color: var(--text-color);
}
.tcp-flag--detached {
  background: var(--error-color);
  color: #fff;
}

.tcp-body {
  margin: 0 0 6px;
  font-size: var(--font-size-base);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.tcp-acts {
  display: flex;
  gap: 5px;
}
.tcp-mini {
  font: var(--font-size-sm) / 1.4 inherit;
  padding: 2px 8px;
  border-radius: var(--radius-sm, 4px);
  border: 1px solid var(--border-color);
  background: var(--bg-color);
  color: var(--text-color);
  cursor: pointer;
}
.tcp-mini--primary {
  background: var(--accent-color);
  border-color: var(--accent-color);
  color: #fff;
}
.tcp-mini--danger {
  color: var(--error-color);
}
.tcp-mini:focus-visible {
  outline: none;
  box-shadow:
    0 0 0 2px var(--focus-ring-gap),
    0 0 0 4px var(--focus-ring);
}

.tcp-reply {
  padding: 10px 12px;
  border-top: 1px solid var(--border-color);
  background: var(--bg-color);
}
.tcp-reply-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-top: 6px;
}
.tcp-hint {
  font-size: var(--font-size-sm);
  color: var(--muted-text);
}
.tcp-mini:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.tcp-input {
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
.tcp-input:focus {
  outline: none;
  border-color: var(--accent-color);
  box-shadow:
    0 0 0 2px var(--focus-ring-gap),
    0 0 0 4px var(--focus-ring);
}

@media (max-width: 640px) {
  .tcp {
    left: 0;
    right: 0;
    width: auto;
  }
}
</style>
