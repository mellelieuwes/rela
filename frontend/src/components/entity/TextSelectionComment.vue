<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount, nextTick, useTemplateRef } from 'vue'
import { useUIStore } from '@/stores'
import { addComment, checkAnchorable } from '@/api/comments'
import { getErrorMessage } from '@/api/errors'

/**
 * Select-to-comment for an entity's markdown body (TKT-FIO205 stage 2).
 *
 * Watches for a text selection inside the body element and offers a floating
 * "Comment" button; accepting opens a small composer anchored to the selection.
 *
 * # Only the quote crosses the wire
 *
 * The browser has RENDERED text; the anchor lives in SOURCE coordinates. Rather
 * than map between them here — which would mean re-implementing the server's
 * render↔source mapping in TypeScript — the client sends the selected string
 * and the server locates it in its own copy of the body. That also means a
 * client cannot describe context the entity does not contain.
 *
 * The rendered text either side of the selection goes with it, so the server can
 * tell WHICH occurrence of a repeated quote was meant — without it, "eordend"
 * selected inside "Geordend" resolves to the earlier "Ongeordend".
 *
 * # Suggesting a change (TKT-S5C0K3)
 *
 * The composer can also propose replacement text. It is pre-filled with the
 * SOURCE markdown the server mapped the selection to (`source_quote`), never
 * the rendered selection: the replacement overwrites source, and editing the
 * rendered form would silently drop any `**` or backtick inside the range.
 */
const props = defineProps<{
  entityType: string
  entityId: string
  /** The rendered body element to watch for selections. */
  container: HTMLElement | null
}>()

const emit = defineEmits<{ added: [] }>()

const uiStore = useUIStore()

/** Minimum selection length, mirroring the server's MinQuoteRunes. */
const MIN_QUOTE_RUNES = 5

const quote = ref('')
/** Rendered text either side of the selection, for occurrence disambiguation. */
const quotePrefix = ref('')
const quoteSuffix = ref('')
const anchorPos = ref<{ top: number; left: number } | null>(null)
/**
 * Why the current selection cannot be commented on, or null when it can.
 *
 * Some selections have no contiguous source range — one spanning table cells is
 * the common case — so the server is asked BEFORE the affordance is offered.
 * Letting someone write a comment that then fails to save is the worse outcome.
 */
const blockedReason = ref<string | null>(null)
const checking = ref(false)
/** Guards against an older check resolving after a newer selection. */
let checkToken = 0
const composing = ref(false)
const body = ref('')
/** The selection's markdown source, from the anchorability check. */
const sourceQuote = ref<string | null>(null)
const suggesting = ref(false)
const replacement = ref('')
/** Posted when a suggestion carries no comment text of its own. */
const DEFAULT_SUGGESTION_BODY = 'Suggested a change.'

const submitting = ref(false)
const composerRef = useTemplateRef<HTMLTextAreaElement>('composer')

function reset() {
  anchorPos.value = null
  composing.value = false
  quote.value = ''
  quotePrefix.value = ''
  quoteSuffix.value = ''
  blockedReason.value = null
  checkToken++
  body.value = ''
  sourceQuote.value = null
  suggesting.value = false
  replacement.value = ''
}

function toggleSuggesting() {
  suggesting.value = !suggesting.value
  // Prefill once; toggling back must not discard what was typed.
  if (suggesting.value && replacement.value === '' && sourceQuote.value !== null) {
    replacement.value = sourceQuote.value
  }
}

/** A suggestion identical to the source would change nothing. */
function canSubmit(): boolean {
  if (suggesting.value) return replacement.value !== sourceQuote.value
  return body.value.trim() !== ''
}

/** How much rendered text to send either side of the selection. */
const CONTEXT_CHARS = 60

/**
 * Rendered text immediately before and after the selection.
 *
 * This is what lets the server pick the right occurrence of a repeated quote.
 * Taken from the container's rendered text — the same coordinate space as the
 * quote itself — so the two agree.
 */
function contextAround(container: HTMLElement, range: Range): { prefix: string; suffix: string } {
  const before = range.cloneRange()
  before.selectNodeContents(container)
  before.setEnd(range.startContainer, range.startOffset)

  const after = range.cloneRange()
  after.selectNodeContents(container)
  after.setStart(range.endContainer, range.endOffset)

  return {
    prefix: before.toString().slice(-CONTEXT_CHARS),
    suffix: after.toString().slice(0, CONTEXT_CHARS),
  }
}

function onSelectionChange() {
  // A selection made while the composer is open must not move the anchor from
  // under it — the user is typing about the text they already picked.
  if (composing.value) return

  const container = props.container
  const sel = window.getSelection()
  if (!container || !sel || sel.isCollapsed || sel.rangeCount === 0) {
    anchorPos.value = null
    return
  }

  const range = sel.getRangeAt(0)
  if (!container.contains(range.commonAncestorContainer)) {
    anchorPos.value = null
    return
  }

  const text = sel.toString().trim()
  if ([...text].length < MIN_QUOTE_RUNES) {
    anchorPos.value = null
    return
  }

  const rect = range.getBoundingClientRect()
  const host = container.getBoundingClientRect()
  quote.value = text
  const ctx = contextAround(container, range)
  quotePrefix.value = ctx.prefix
  quoteSuffix.value = ctx.suffix
  anchorPos.value = {
    top: rect.bottom - host.top + 6,
    left: Math.max(0, rect.left - host.left),
  }
  void verifySelection()
}

/** Asks the server whether this selection can anchor, before offering to comment. */
async function verifySelection() {
  const token = ++checkToken
  blockedReason.value = null
  sourceQuote.value = null
  checking.value = true
  try {
    const res = await checkAnchorable(
      props.entityType,
      props.entityId,
      quote.value,
      quotePrefix.value,
      quoteSuffix.value
    )
    // Discard a stale answer: the user may have re-selected while this was in
    // flight, and applying it would describe the wrong selection.
    if (token !== checkToken) return
    blockedReason.value = res.anchorable
      ? null
      : res.reason || 'This selection cannot be commented on'
    // Only offered where a replacement would be accepted: one spanning list
    // items or paragraphs is refused on submit.
    sourceQuote.value = res.anchorable && res.suggestable ? (res.source_quote ?? null) : null
  } catch {
    // A failed CHECK must not block commenting: the create path validates
    // again, so the worst case is the old behaviour (an error on save).
    if (token === checkToken) blockedReason.value = null
  } finally {
    if (token === checkToken) checking.value = false
  }
}

async function startComposing() {
  composing.value = true
  await nextTick()
  composerRef.value?.focus()
}

async function submit() {
  if (!canSubmit() || submitting.value) return
  const text = body.value.trim() || DEFAULT_SUGGESTION_BODY
  submitting.value = true
  try {
    await addComment(props.entityType, props.entityId, {
      anchor: {
        kind: 'text',
        ref: '',
        quote: quote.value,
        quote_prefix: quotePrefix.value,
        quote_suffix: quoteSuffix.value,
        ...(suggesting.value ? { replacement: replacement.value } : {}),
      },
      body: text,
    })
    reset()
    window.getSelection()?.removeAllRanges()
    emit('added')
  } catch (err) {
    // The most likely failure is a stale tab: the body changed since it was
    // rendered, so the quote no longer exists server-side. The message says so.
    uiStore.error(getErrorMessage(err))
  } finally {
    submitting.value = false
  }
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    reset()
  }
}

onMounted(() => {
  document.addEventListener('selectionchange', onSelectionChange)
  document.addEventListener('keydown', onKeydown)
})

onBeforeUnmount(() => {
  document.removeEventListener('selectionchange', onSelectionChange)
  document.removeEventListener('keydown', onKeydown)
})
</script>

<template>
  <div
    v-if="anchorPos"
    class="tsc"
    :style="{ top: `${anchorPos.top}px`, left: `${anchorPos.left}px` }"
    @mousedown.prevent
  >
    <!-- Blocked: say why, and do not offer a composer that cannot succeed. -->
    <span v-if="!composing && blockedReason" class="tsc-blocked" :title="blockedReason">
      <svg viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
        <path d="M8 1.5 15 14H1L8 1.5Zm0 4.5v4m0 2v.5" stroke="currentColor" fill="none" />
      </svg>
      Can't comment here
    </span>

    <button
      v-else-if="!composing"
      type="button"
      class="tsc-btn"
      :disabled="checking"
      @click="startComposing"
    >
      <svg viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
        <path d="M8 1a7 7 0 0 0-6.1 10.4L1 15l3.8-.9A7 7 0 1 0 8 1Z" />
      </svg>
      {{ checking ? 'Checking…' : 'Comment' }}
    </button>

    <form v-else class="tsc-form" @submit.prevent="submit">
      <blockquote class="tsc-quote">{{ quote }}</blockquote>
      <label v-if="suggesting" class="tsc-label" for="tsc-replacement">Replace with</label>
      <textarea
        v-if="suggesting"
        id="tsc-replacement"
        v-model="replacement"
        class="tsc-input tsc-replacement"
        rows="3"
        aria-label="Suggested replacement"
        @keydown.meta.enter="submit"
        @keydown.ctrl.enter="submit"
      />
      <textarea
        ref="composer"
        v-model="body"
        class="tsc-input"
        rows="3"
        :placeholder="suggesting ? 'Explain the change (optional)…' : 'Add a comment…'"
        aria-label="Comment body"
        @keydown.meta.enter="submit"
        @keydown.ctrl.enter="submit"
      />
      <div class="tsc-actions">
        <button
          v-if="sourceQuote !== null"
          type="button"
          class="tsc-suggest"
          :aria-pressed="suggesting"
          @click="toggleSuggesting"
        >
          {{ suggesting ? 'Comment only' : 'Suggest a change' }}
        </button>
        <span class="tsc-hint">⌘↵ to post</span>
        <button type="button" class="tsc-cancel" @click="reset">Cancel</button>
        <button type="submit" class="tsc-submit" :disabled="submitting || !canSubmit()">
          {{ submitting ? 'Adding…' : suggesting ? 'Suggest' : 'Comment' }}
        </button>
      </div>
    </form>
  </div>
</template>

<style scoped>
.tsc {
  position: absolute;
  z-index: 25;
}

.tsc-btn {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 5px 10px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius-md, 6px);
  background: var(--card-bg);
  color: var(--text-color);
  box-shadow: var(--shadow-lg, 0 4px 12px rgb(0 0 0 / 12%));
  cursor: pointer;
  font: 600 var(--font-size-sm) / 1 inherit;
}
.tsc-btn svg {
  width: 12px;
  height: 12px;
  color: var(--accent-color);
}
.tsc-btn:hover:not(:disabled) {
  border-color: var(--accent-color);
}
.tsc-btn:disabled {
  opacity: 0.7;
  cursor: default;
}

/* Shown in place of the Comment button when the selection cannot be anchored,
 * so the reason is visible BEFORE anything is typed. */
.tsc-blocked {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 5px 10px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius-md, 6px);
  background: var(--card-bg);
  color: var(--muted-text);
  box-shadow: var(--shadow-lg, 0 4px 12px rgb(0 0 0 / 12%));
  font: 600 var(--font-size-sm) / 1 inherit;
  cursor: help;
}
.tsc-blocked svg {
  width: 12px;
  height: 12px;
  color: var(--warning-color);
}

.tsc-form {
  width: 320px;
  padding: 10px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius-lg, 8px);
  background: var(--card-bg);
  box-shadow: var(--shadow-lg, 0 10px 30px rgb(0 0 0 / 16%));
}

/* The quoted text, so the composer states what is being commented on — the
 * selection highlight is lost the moment focus moves to the textarea. */
.tsc-quote {
  margin: 0 0 8px;
  padding: 4px 8px;
  border-left: 3px solid var(--accent-color);
  background: var(--bg-color);
  color: var(--muted-text);
  font-size: var(--font-size-sm);
  font-style: italic;
  max-height: 3.6em;
  overflow: hidden;
}

.tsc-input {
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
.tsc-input:focus {
  outline: none;
  border-color: var(--accent-color);
  box-shadow:
    0 0 0 2px var(--focus-ring-gap),
    0 0 0 4px var(--focus-ring);
}

.tsc-actions {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 7px;
}
.tsc-label {
  display: block;
  margin-bottom: 3px;
  font-size: var(--font-size-sm);
  font-weight: 600;
  color: var(--muted-text);
}
.tsc-replacement {
  margin-bottom: 6px;
  font-family: var(--font-mono, monospace);
}
.tsc-suggest {
  padding: 4px 8px;
  border: 1px solid var(--border-color);
  border-radius: var(--radius-sm, 5px);
  background: var(--bg-color);
  color: var(--accent-color);
  font: 600 var(--font-size-sm) / 1.4 inherit;
  cursor: pointer;
}
.tsc-hint {
  margin-right: auto;
  font-size: var(--font-size-sm);
  color: var(--muted-text);
}
.tsc-cancel,
.tsc-submit {
  padding: 4px 11px;
  border-radius: var(--radius-sm, 5px);
  font: 600 var(--font-size-sm) / 1.4 inherit;
  cursor: pointer;
}
.tsc-cancel {
  border: 1px solid var(--border-color);
  background: var(--bg-color);
  color: var(--text-color);
}
.tsc-submit {
  border: 1px solid var(--accent-color);
  background: var(--accent-color);
  color: #fff;
}
.tsc-submit:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}
</style>
