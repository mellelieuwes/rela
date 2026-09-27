<script setup lang="ts">
/**
 * The WYSIWYG markdown editor.
 *
 * Public contract is unchanged from the editor it replaces: `modelValue` in,
 * `update:modelValue` out. `DynamicForm` needs no changes.
 *
 * Three things here are load-bearing:
 *
 * 1. Serialization is pinned by `RELA_STRINGIFY_OPTIONS` and checked against
 *    every entity body in the repository by the corpus test. `guardWriteBack`
 *    then suppresses the reformatting that survives, so opening an entity and
 *    saving it without edits emits nothing.
 * 2. Entity references are `entityRef` nodes that serialize back to the exact
 *    code span they came from. Titles are view-only attributes and never reach
 *    the markdown.
 * 3. Titles come only from the injected `refResolver`, which is fed by the
 *    server's per-principal `mentions` map. The editor never derives a title
 *    from the graph itself.
 */
import { ref, onMounted, onBeforeUnmount, watch, shallowRef, computed, nextTick } from 'vue'
import { Editor, rootCtx, defaultValueCtx, editorViewOptionsCtx } from '@milkdown/kit/core'
import { gfm } from '@milkdown/kit/preset/gfm'
import { history } from '@milkdown/kit/plugin/history'
import { block, BlockProvider } from '@milkdown/kit/plugin/block'
import { cursor } from '@milkdown/kit/plugin/cursor'
import { trailing } from '@milkdown/kit/plugin/trailing'
import { listener, listenerCtx } from '@milkdown/kit/plugin/listener'
import { SlashProvider, slashFactory } from '@milkdown/kit/plugin/slash'
import { positionLinkPanel } from './linkPanelPosition'
import { replaceAll, getMarkdown, callCommand, $prose } from '@milkdown/kit/utils'
import { editorViewCtx } from '@milkdown/kit/core'
import type { EditorView } from '@milkdown/kit/prose/view'
import { Plugin, PluginKey } from '@milkdown/kit/prose/state'
import type { EditorState } from '@milkdown/kit/prose/state'
import { lift } from '@milkdown/kit/prose/commands'
import '@milkdown/kit/prose/view/style/prosemirror.css'
import '@milkdown/kit/prose/tables/style/tables.css'
import '@milkdown/kit/prose/gapcursor/style/gapcursor.css'
import './milkdownEditor.css'

import { RELA_COMMONMARK, RELA_OUTPUT_NODES, configureRelaSerializer } from './editorPreset'
import { entityRefNode } from './entityRefNode'
import { insertEntityRefAtCursor } from './insertEntityRef'
import { taskList } from './taskListItem'
import {
  entityRefResolutionPlugin,
  buildResolutionTransaction,
  isResolutionTransaction,
  type ResolverHandle,
} from './entityRefResolution'
import { guardWriteBack, decideEmit } from './writeBackGuard'
import { useEditorMention } from './useEditorMention'
import type { MentionSelf } from './useMentionMenu'
import {
  INLINE_COMMANDS,
  BLOCK_COMMANDS,
  HISTORY_COMMANDS,
  type EditorCommand,
} from './editorCommands'
import { undoDepth, redoDepth } from '@milkdown/kit/prose/history'
import { linkPaste } from './linkPaste'
import { useLinkUI } from './useLinkUI'
import LinkDialog from './LinkDialog.vue'
import LinkTooltip from './LinkTooltip.vue'
import { activeCommandIds } from './activeFormats'
import { unavailableCommandIds } from './commandAvailability'
import {
  TABLE_COMMANDS,
  DIRECT_TABLE_COMMANDS,
  GUARDED_TABLE_COMMANDS,
  canRunTableCommand,
  canAddRowBefore,
  runTableCommand,
  isInTable,
} from './tableCommands'
import MentionMenu from './MentionMenu.vue'
import EditorToolbar from './EditorToolbar.vue'
import EntityPickerModal from '../EntityPickerModal.vue'
import type { EntityRefResolver } from '@/utils/markdown'
import { useUIStore } from '@/stores/ui'

const props = defineProps<{
  modelValue: string
  placeholder?: string
  /**
   * Resolves an entity ID to its title and type, for display only.
   *
   * Supplied by the form from the server's `mentions` map, which is computed
   * per principal through the read gate. Absent means every reference renders
   * as its bare ID, which is the correct degraded state rather than an error.
   */
  refResolver?: EntityRefResolver
  /**
   * The entity whose body this is, or absent for a new entity. The `@` menu
   * starts from its related entities and never offers it to itself.
   */
  mentionSelf?: MentionSelf
}>()

const emit = defineEmits<{
  'update:modelValue': [value: string]
}>()

const editorRoot = ref<HTMLElement | null>(null)
const menuRoot = ref<HTMLElement | null>(null)
const blockHandleRoot = ref<HTMLElement | null>(null)
const editor = shallowRef<Editor | null>(null)

/** True while the document is empty, so the placeholder shows. */
const isEmpty = ref(true)

// The `@` menu; see `useEditorMention`. `slashProvider` is assigned at mount,
// so the host reads it lazily.
const mention = useEditorMention(() => props.mentionSelf ?? null, {
  view: () => currentView(),
  textBefore: (view) => slashProvider?.getContent(view),
  hide: () => slashProvider?.hide(),
})
const menu = mention.menu

const showPlaceholder = computed(() => isEmpty.value)

/**
 * Command ids whose formatting is active at the cursor.
 *
 * Recomputed from a ProseMirror plugin on every state change rather than
 * polled, so the toolbar tracks the selection as it moves.
 */
const activeIds = ref<Set<string>>(new Set())

/**
 * Command ids that would do nothing if pressed, so their buttons disable.
 *
 * Heading inside a list item is the motivating case: the command cannot apply
 * there, and a button that looks pressable but no-ops gives the user nothing
 * to reason about.
 */
const unavailableIds = ref<Set<string>>(new Set())

const inlineCommands = INLINE_COMMANDS
/** The toolbar's block buttons. The `/` menu offers the same set, filtered. */
const toolbarBlockCommands = BLOCK_COMMANDS
/** The table group, shown only while the cursor is inside a table. */
const tableCommands = TABLE_COMMANDS
/** Undo and redo, in their own group at the end of the toolbar. */
const historyCommands = HISTORY_COMMANDS
/**
 * Whether to show the table group at all.
 *
 * Hidden rather than disabled: seven permanently-greyed buttons is a lot of
 * dead chrome to carry on every paragraph, and unlike the block commands
 * these have no meaning outside a table to hint at.
 */
const showTableGroup = ref(false)

/** Every command the toolbar can light up, probed together on each change. */
const ALL_COMMANDS = [...INLINE_COMMANDS, ...BLOCK_COMMANDS, ...TABLE_COMMANDS, ...HISTORY_COMMANDS]

const linkUI = useLinkUI()

/** The element the link panel's floating provider positions. */
const linkPanelRoot = ref<HTMLElement | null>(null)

const uiStore = useUIStore()

/**
 * Tells the user their change was refused, once.
 *
 * `drift-blocked` means re-serializing produced markdown that MEANS something
 * different from what was loaded, so writing it back would corrupt the body.
 * Refusing is right; refusing quietly is not — the form would look saved
 * while the edit was dropped. Repeated once per editing session rather than
 * per keystroke, because the condition persists until the document changes.
 */
let driftReported = false
function reportDrift(): void {
  if (driftReported) return
  driftReported = true
  uiStore.error(
    'This content could not be saved safely: converting it back to markdown ' +
      'would change its meaning. Your edit has not been applied. Please report this.'
  )
}

/**
 * The EXACT bytes the parent last handed us, never re-derived.
 *
 * This is the guard's reference point, and it must stay pristine. An earlier
 * version re-baselined it to `serialize()` once loading settled, which looked
 * reasonable and quietly destroyed the guard: comparing the round-tripped
 * form against itself always says "unchanged", so churn was reported as clean
 * and written back. A body whose first heading was setext came back as ATX
 * with the guard insisting nothing had happened.
 *
 * Only the two places a new value arrives from outside may assign it: the
 * initial prop, and the reload watcher.
 */
let originalValue = props.modelValue

/**
 * The markdown the editor holds once loading has settled.
 *
 * Distinct from `originalValue`: this one IS re-derived after load, and its
 * only job is to suppress the listener's echo of our own serialization. It
 * must never reach `guardWriteBack`.
 */
let settledValue = props.modelValue

/**
 * The last value emitted to the parent, or null if none.
 *
 * The parent holds this, not `originalValue`, once anything has been emitted.
 * Without it, a user who edits and then reverts produces markdown equal to the
 * original, the emit is skipped as "nothing to say", and the parent saves the
 * intermediate value it was last told about.
 */
let lastEmitted: string | null = null
/**
 * Whether the user has changed the document since it was loaded.
 *
 * Set from a ProseMirror plugin rather than from the markdown listener,
 * because not every edit reaches that listener the same way: an insertion
 * dispatched directly on the view (the toolbar picker) changes the document
 * without going through the typing path. Reading `docChanged` catches every
 * route into the document, and it is what `guardWriteBack` needs to tell a
 * real edit from a round-trip artifact. Getting this wrong discards the edit.
 *
 * Resolution transactions are excluded: a title arriving from the server
 * changes node attributes, not content, and must not count as an edit. So is
 * everything before `armed` — see the tracker plugin for why.
 */
let dirty = false

/**
 * Whether document changes now count as user edits.
 *
 * False while a document is being loaded, because loading provokes
 * normalization that is not an edit.
 */
let armed = false

// Held in a mutable box so the ProseMirror plugin, created once at mount,
// always reads the current resolver rather than the one captured at build.
const resolverHandle: ResolverHandle = { resolver: props.refResolver }

const dirtyTrackerKey = new PluginKey('rela-dirty-tracker')

let slashProvider: SlashProvider | null = null
/** Tears down the link panel's floating-ui autoUpdate subscription. */
let cleanupLinkPanel: (() => void) | null = null
let blockProvider: BlockProvider | null = null

/** True when the document holds nothing a user has written. */
function isDocEmpty(state: EditorState): boolean {
  const { doc } = state
  if (doc.childCount === 0) return true
  if (doc.childCount > 1) return false
  const first = doc.firstChild
  return first !== null && first.type.name === 'paragraph' && first.content.size === 0
}

/**
 * Recomputes everything the UI derives from editor state.
 *
 * Called from the tracker plugin on every transaction AND directly after a
 * load, because `appendTransaction` does not run for the initial document.
 */
function refreshDerivedState(state: EditorState): void {
  isEmpty.value = isDocEmpty(state)
  const active = activeCommandIds(state, ALL_COMMANDS)
  activeIds.value = active
  showTableGroup.value = isInTable(state)
  const e = editor.value
  if (!e) return

  const unavailable = unavailableCommandIds(
    e.ctx,
    state,
    ALL_COMMANDS.filter(
      (c) => !DIRECT_TABLE_COMMANDS.has(c.id) && !GUARDED_TABLE_COMMANDS.has(c.id)
    ),
    active,
    lift(state)
  )
  // The deletes are not registered slices, so they answer for themselves.
  for (const cmd of ALL_COMMANDS) {
    if (DIRECT_TABLE_COMMANDS.has(cmd.id) && !canRunTableCommand(cmd.id, state)) {
      unavailable.add(cmd.id)
    }
  }
  // AddRowBefore IS a slice, and it claims to apply in the header row before
  // corrupting the table. Its own guard overrides the dry run.
  if (!canAddRowBefore(state)) unavailable.add('addRowBefore')

  // Undo and redo report themselves applicable whether or not there is
  // anything on the stack, so the depth is the only honest answer. Same
  // shape of override as the table guards above.
  if (undoDepth(state) === 0) unavailable.add('undo')
  if (redoDepth(state) === 0) unavailable.add('redo')

  unavailableIds.value = unavailable

  // Which link the caret is in, for the toolbar's unlink button and the
  // floating panel. A read, never a dispatch: writing here would mark the
  // document dirty on a mere cursor move and put a diff in git for an entity
  // somebody only opened.
  linkUI.refresh(state)
  repositionLinkPanel()
}

/**
 * Re-anchors the link panel to the link it is describing.
 *
 * Runs after `linkUI.refresh`, so it positions against the link THIS state
 * implies rather than the previous one — the lag that made the panel appear
 * at the last place the cursor was.
 */
function repositionLinkPanel(): void {
  cleanupLinkPanel?.()
  cleanupLinkPanel = null

  const view = currentView()
  const panel = linkPanelRoot.value
  if (!view || !panel) return

  const link = linkUI.panelLink.value
  cleanupLinkPanel = positionLinkPanel(view, panel, link ? { from: link.from, to: link.to } : null)
}

function currentView(): EditorView | null {
  const e = editor.value
  if (!e) return null
  try {
    return e.ctx.get(editorViewCtx)
  } catch {
    return null
  }
}

// The toolbar's entity-reference picker, kept from the editor this replaces.
// The `@` menu is the fast path; this stays for discovery, since a user who
// has not learned the trigger still needs a way in.
const pickerOpen = ref(false)

function onPickerSelect(id: string): void {
  const view = currentView()
  if (!view) return
  // The picker emits an ID only, so the title resolves through the mentions
  // map on the next pass, or falls back to the ID. It cannot be looked up
  // locally without routing around the read gate.
  const hit = resolverHandle.resolver?.(id) ?? null
  insertEntityRefAtCursor(view, {
    id,
    title: hit?.title ?? '',
    entityType: hit?.type ?? null,
  })
}

function onPickerClose(): void {
  pickerOpen.value = false
  void nextTick(() => currentView()?.focus())
}

function onLinkSubmit(payload: { url: string; text: string; strippedParams: boolean }): void {
  const view = currentView()
  if (!view) return
  linkUI.closeDialog()
  linkUI.submit(view, payload)
  // Silently changing what someone typed is worse than refusing it, so say so
  // when a mailto's parameters were dropped.
  if (payload.strippedParams) {
    uiStore.showToast('info', 'Email parameters were removed from the link.')
  }
}

function onLinkUnlink(): void {
  const view = currentView()
  if (!view) return
  linkUI.unlink(view)
}

/**
 * Runs a formatting command and returns focus to the document.
 *
 * A toolbar button steals focus on mousedown unless prevented, and even with
 * that the command must run against a focused view for the selection to be
 * where the user left it. Refocusing after dispatch covers both.
 */
function runCommand(cmd: EditorCommand): void {
  const e = editor.value
  if (!e) return
  if (unavailableIds.value.has(cmd.id)) return

  // A link needs a target before anything can be applied, so the button opens
  // the dialog instead of dispatching. It must NOT fall through to
  // `ToggleLink`: that is `toggleMark`, which over a selection merely touching
  // an existing link removes it. `openForSelection` routes that case to a
  // retarget.
  if (cmd.id === 'link') {
    const view = currentView()
    if (view) linkUI.openForSelection(view)
    return
  }

  // Row/column deletes bypass the command manager; see `tableCommands`.
  if (DIRECT_TABLE_COMMANDS.has(cmd.id)) {
    const view = currentView()
    if (view) {
      runTableCommand(cmd.id, view.state, view.dispatch.bind(view))
      view.focus()
    }
    return
  }

  // An active block command runs its inverse, so the button toggles rather
  // than no-opping on a block that is already that type.
  const active = activeIds.value.has(cmd.id)
  if (active && cmd.toggleTo === 'lift') {
    // Unwrapping a blockquote has no preset command; see `toggleTo`.
    const view = currentView()
    if (view) lift(view.state, view.dispatch)
  } else if (active && cmd.toggleTo) {
    e.action(callCommand(cmd.toggleTo))
  } else {
    e.action(callCommand(cmd.command, cmd.payload))
  }
  currentView()?.focus()
}

function onKeydownCapture(event: KeyboardEvent): void {
  // The mention handler goes first and unconditionally: it can open a menu the
  // debounced slash update has not opened yet, so `menu.state.open` is only
  // meaningful after it ran.
  mention.onKeydown(event)
  if (event.defaultPrevented || menu.state.open) return
  if (event.key === 'Escape' && linkUI.panelOpen.value) {
    event.preventDefault()
    // Remembering the dismissal is what makes this work at all. Simply
    // hiding the panel would achieve nothing: its visibility is derived
    // from the selection, and the caret is still in the link, so the next
    // state change would show it again and Escape would look broken.
    linkUI.dismiss()
  }
}

onMounted(async () => {
  if (!editorRoot.value || !menuRoot.value) return

  const slash = slashFactory('rela-entity-mention')

  // Marks the editor dirty on a document change the USER caused.
  //
  // `docChanged` alone is not that signal. Loading a document runs
  // normalization of its own: with a heading and a table in the same body,
  // ProseMirror's table plugin rewrites the column widths before the user has
  // touched anything. Counting that as an edit told the write-back guard the
  // body had been edited, so it wrote the reformatted table back and put a
  // diff in git for an entity that was only opened.
  //
  // `armed` is what separates the two. It is set once loading has settled, so
  // load-time normalization is ignored and everything after it is real. A
  // reload re-disarms for the same reason.
  const dirtyTracker = $prose(
    () =>
      new Plugin({
        key: dirtyTrackerKey,
        appendTransaction: (transactions, _oldState, newState) => {
          for (const tr of transactions) {
            if (armed && tr.docChanged && !isResolutionTransaction(tr)) dirty = true
          }
          // Recomputed here rather than in a watcher so the toolbar updates
          // on selection moves too, which change state without changing the
          // document.
          refreshDerivedState(newState)
          return null
        },
      })
  )

  editor.value = await Editor.make()
    .config((ctx) => {
      ctx.set(rootCtx, editorRoot.value as HTMLElement)
      ctx.set(defaultValueCtx, props.modelValue)
      // `md-body` is the marker class for the app's shared markdown stylesheet
      // (styles/markdown-content.css), the documented single source of truth
      // for how a rendered body looks. Wearing it means the editing surface
      // inherits the exact headings, lists, tables, quotes and code styling
      // the entity view will show, so WYSIWYG is true by construction instead
      // of by a parallel set of rules that would drift.
      ctx.update(editorViewOptionsCtx, (prev) => ({
        ...prev,
        attributes: { ...(prev.attributes ?? {}), class: 'milkdown-prose md-body' },
      }))
      // Shared with the sandboxed app editor, so the two cannot serialize a
      // body differently.
      configureRelaSerializer(ctx)

      // The guard sits ON the emit rather than beside it. Exposing it as an
      // optional `guardedValue()` for the form to prefer meant the raw channel
      // was the default and the guarded one a side door nobody walked through,
      // so every round-trip artifact reached the save path. There is now no
      // unguarded route out of this component.
      ctx.get(listenerCtx).markdownUpdated((_ctx, markdown) => {
        const decision = decideEmit(markdown, originalValue, settledValue, dirty, lastEmitted)
        if (decision.action === 'report-drift') {
          // Refusing silently would lose the user's edit while the form still
          // looked saved, which is worse than the churn this module prevents.
          reportDrift()
          return
        }
        if (decision.action === 'emit') {
          lastEmitted = decision.value
          emit('update:modelValue', decision.value)
        }
      })

      ctx.set(slash.key, {
        view: () => {
          slashProvider = new SlashProvider({
            content: menuRoot.value as HTMLElement,
            trigger: '@',
            debounce: 0,
            shouldShow: (view) => mention.shouldShow(view),
          })
          return {
            update: (view, prevState) => slashProvider?.update(view, prevState),
            destroy: () => slashProvider?.destroy(),
          }
        },
      })
    })
    .use(RELA_COMMONMARK)
    .use(gfm)
    .use(history)
    .use(listener)
    .use(entityRefNode)
    .use(RELA_OUTPUT_NODES)
    // After `gfm`: it contributes the `checked` attribute this renders.
    .use(taskList)
    .use(entityRefResolutionPlugin(resolverHandle))
    // After the presets, so its `handlePaste` is offered the event before
    // theirs. It declines everything that is not a bare URL over a selection,
    // so ordinary pasting is untouched.
    .use(linkPaste)
    .use(dirtyTracker)
    .use(mention.plugin)
    .use(slash)
    // Drag/insert handle in the gutter, plus the drop cursor and gap cursor it
    // needs to be usable: without `cursor` a drag has no visible target, and
    // without `trailing` there is no way to click below a table or code block
    // that ends the document to start a new paragraph.
    .use(block)
    .use(cursor)
    .use(trailing)
    .create()

  // The gutter handle. Built after creation because it needs the editor's ctx.
  // `getOffset` pushes it into the left gutter the shell reserves, so it sits
  // beside the block rather than on top of the first characters.
  if (blockHandleRoot.value && editor.value) {
    blockProvider = new BlockProvider({
      ctx: editor.value.ctx,
      content: blockHandleRoot.value,
      getOffset: () => ({ mainAxis: 8 }),
      getPlacement: () => 'left-start',
    })
  }

  // The link panel needs no setup here: `repositionLinkPanel` anchors it from
  // `refreshDerivedState`, and it stays where the template put it — inside the
  // shell, outside the editable div. See `linkPanelPosition.ts` for why it is
  // positioned directly rather than through `TooltipProvider`.

  applyResolver()
  const mountedView = currentView()
  // Seed the derived state from the loaded document. `appendTransaction` only
  // runs once a transaction is dispatched, so without this the toolbar shows
  // nothing active until the first edit — a document opened straight onto a
  // heading or a quote had the wrong buttons lit.
  if (mountedView) refreshDerivedState(mountedView.state)
  // Loading is done; from here a document change is the user's doing.
  // Only the echo-suppression value settles here. `originalValue` keeps the
  // bytes the parent gave us, which is what the guard has to compare against.
  settledValue = serialize()
  armed = true

  // E2E hook: expose the serialized markdown on the editor root.
  //
  // The markdown is a serialization of the ProseMirror document, not a buffer
  // hanging off a DOM node, so there is nothing for a test to read the way it
  // could read CodeMirror's `getValue`. Compiled out of production builds by
  // the `__E2E_TEST_HOOKS__` define, so neither the hook nor its name ships
  // (issue #890).
  if (__E2E_TEST_HOOKS__ && editorRoot.value) {
    ;(editorRoot.value as HTMLElement & { __relaGetMarkdown?: () => string }).__relaGetMarkdown =
      () => serialize()
  }
})

/** Re-runs resolution over the whole document against the current resolver. */
function applyResolver(): void {
  const view = currentView()
  if (!view) return
  const tr = buildResolutionTransaction(view.state, resolverHandle.resolver)
  if (tr) view.dispatch(tr)
}

watch(
  () => props.refResolver,
  (next) => {
    resolverHandle.resolver = next
    applyResolver()
  }
)

watch(
  () => props.modelValue,
  (next) => {
    const e = editor.value
    if (!e) return
    // Only reload when the parent's value genuinely diverges from what the
    // editor holds. Without the guard, echoing back our own emitted value
    // would rebuild the document and drop the cursor on every keystroke.
    const view = currentView()
    if (!view) return
    const currentMarkdown = serialize()
    if (currentMarkdown === next) return
    // A genuinely new body from the parent: this is now the pristine baseline.
    originalValue = next
    settledValue = next
    lastEmitted = null
    dirty = false
    driftReported = false
    armed = false
    e.action(replaceAll(next))
    applyResolver()
    // Same reason as at mount: a reload replaces the document without the
    // tracker having seen a transaction for the new content.
    const reloaded = currentView()
    if (reloaded) refreshDerivedState(reloaded.state)
    // Same reasoning as at mount: only the echo-suppression value settles.
    settledValue = serialize()
    armed = true
  }
)

/**
 * The markdown the editor currently holds.
 *
 * Falls back to the last loaded value if the editor is not ready, so a caller
 * never sees an empty body for an entity that has content.
 */
function serialize(): string {
  const e = editor.value
  if (!e) return originalValue
  try {
    return e.action(getMarkdown())
  } catch {
    return originalValue
  }
}

defineExpose({
  /**
   * Pushes any pending change to the parent immediately.
   *
   * Milkdown's markdown listener is debounced by 200ms, so a change made and
   * saved within that window never reaches the parent: the entity-reference
   * picker inserts on a direct view dispatch, and submitting straight after
   * stored the body WITHOUT the reference. A form must call this before
   * reading its content model.
   *
   * Runs the same decision as the listener, so a flush cannot bypass the
   * write-back guard.
   */
  flush: () => {
    const markdown = serialize()
    const decision = decideEmit(markdown, originalValue, settledValue, dirty, lastEmitted)
    if (decision.action === 'report-drift') {
      reportDrift()
      return
    }
    if (decision.action === 'emit') {
      lastEmitted = decision.value
      emit('update:modelValue', decision.value)
    }
  },
  /**
   * The markdown to save, with round-trip churn suppressed.
   *
   * The emit path already applies this, so the form does not need to call it.
   * Kept exposed because it is the only way a test can read the verdict, and
   * a verdict-level assertion is what pins the churn/drift distinction.
   */
  guardedValue: () => guardWriteBack(originalValue, serialize(), dirty),
  /**
   * The live ProseMirror view, for tests only.
   *
   * Two things can only be checked against the real editor. The active-state
   * probes name marks and nodes by string and a miss returns false rather
   * than throwing, so a rename upstream would quietly stop the toolbar
   * lighting up; and a command's slice name is resolved at call time, so a
   * wrong one throws only when the button is pressed. A hand-built schema
   * cannot catch either, since it would be renamed alongside the code.
   *
   * Tests also need it to place a real selection, which is the case a
   * cursor-only test does not cover.
   */
  get editorViewForTest() {
    return currentView()
  },
  get editorInstanceForTest() {
    return editor.value
  },
})

onBeforeUnmount(() => {
  // Close the picker before tearing the editor down so a late `select` cannot
  // fire against a destroyed view.
  pickerOpen.value = false
  mention.dispose()
  slashProvider?.destroy()
  slashProvider = null
  cleanupLinkPanel?.()
  cleanupLinkPanel = null
  blockProvider?.destroy()
  blockProvider = null
  void editor.value?.destroy()
  editor.value = null
})
</script>

<template>
  <div class="milkdown-editor-shell">
    <EditorToolbar
      :inline-commands="inlineCommands"
      :block-commands="toolbarBlockCommands"
      :active-ids="activeIds"
      :unavailable-ids="unavailableIds"
      :table-commands="tableCommands"
      :show-table-group="showTableGroup"
      :history-commands="historyCommands"
      :show-unlink="linkUI.inLink.value"
      @run="runCommand"
      @open-entity-picker="pickerOpen = true"
      @unlink="onLinkUnlink"
    />

    <div class="milkdown-editor-body">
      <div ref="editorRoot" class="milkdown-editor" @keydown.capture="onKeydownCapture">
        <!-- Placeholder for an empty document. A sibling element rather than a
             `::before` fed by `attr()`: the text would have to reach the
             pseudo-element through an inline `style`, and the sandboxed-app CSP
             this editor is headed for (`style-src` with no `'unsafe-inline'`)
             blocks the `style` attribute outright. A real element needs no
             inline style, so Phase 4 does not have to undo this.

             `aria-hidden` because ProseMirror's own textbox role already names
             the field; a screen reader announcing both would read it twice. -->
        <div v-if="showPlaceholder" class="milkdown-placeholder" aria-hidden="true">
          {{ props.placeholder || 'Markdown content...' }}
        </div>
      </div>
    </div>

    <!-- The gutter handle BlockProvider positions against the hovered block.
         Purely a drag affordance: the grip is what ProseMirror's block service
         binds its drag events to. -->
    <div ref="blockHandleRoot" class="block-handle" data-show="false">
      <span class="block-handle-grip" aria-hidden="true">
        <svg viewBox="0 0 10 16" width="10" height="16" fill="currentColor">
          <circle cx="2.5" cy="3" r="1.3" />
          <circle cx="7.5" cy="3" r="1.3" />
          <circle cx="2.5" cy="8" r="1.3" />
          <circle cx="7.5" cy="8" r="1.3" />
          <circle cx="2.5" cy="13" r="1.3" />
          <circle cx="7.5" cy="13" r="1.3" />
        </svg>
      </span>
    </div>

    <!-- Outside the editable div on purpose. SlashProvider positions THIS
         element (it is what `content` is set to) and appends it to the
         editor's parent; leaving it inside the contenteditable would make the
         menu part of the document the user is editing. -->
    <div ref="menuRoot" class="mention-menu-anchor" data-show="false">
      <MentionMenu
        :state="menu.state"
        :highlighted-index="menu.highlightedIndex()"
        @pick="mention.onPick"
        @hover="mention.onHover"
      />
    </div>

    <!-- Outside the editable div for the same reason as the mention menu: the
         provider positions this element, and inside the contenteditable it
         would become part of the document being edited. -->
    <div ref="linkPanelRoot" class="link-panel-anchor" data-show="false">
      <LinkTooltip
        v-if="linkUI.panelLink.value"
        :href="linkUI.panelLink.value.href"
        @edit="linkUI.openForPanel()"
        @unlink="onLinkUnlink"
      />
    </div>

    <EntityPickerModal :open="pickerOpen" @select="onPickerSelect" @close="onPickerClose" />

    <LinkDialog
      :open="linkUI.dialog.value.open"
      :initial-url="linkUI.dialog.value.initialUrl"
      :initial-text="linkUI.dialog.value.initialText"
      :needs-text="linkUI.dialog.value.needsText"
      :editing="linkUI.dialog.value.editing"
      @submit="onLinkSubmit"
      @close="linkUI.closeDialog()"
    />
  </div>
</template>
