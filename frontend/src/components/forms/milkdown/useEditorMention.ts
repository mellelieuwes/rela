/**
 * The `@` menu's wiring into one Milkdown editor.
 *
 * Joins the pieces that each own one concern: `mentionArm` (was the `@`
 * typed?), `mentionTrigger` (should the menu show?),
 * `useMentionMenu` (what the menu shows), `mentionKeymap` (what a key means),
 * and `insertEntityRef` (what choosing a row writes). Extracted from
 * `MilkdownEditor.vue`, which is over the component size limit, so the editor
 * only mounts the plugin, binds the handlers and renders the menu.
 */
import { watch } from 'vue'
import { $prose } from '@milkdown/kit/utils'
import type { EditorView } from '@milkdown/kit/prose/view'
import type { Entity } from '@/types'
import { entityDisplayTitle } from '@/utils/entityDisplay'
import { recordRecentEntity } from '@/utils/recentEntities'
import { replaceMentionQueryWithRef, replaceMentionQueryWithText } from './insertEntityRef'
import { armedAt, armedToken, mentionArmPlugin, type ArmedToken } from './mentionArm'
import { createMentionKeydownHandler } from './mentionKeymap'
import { scopeText } from './mentionPlan'
import { createMentionTrigger } from './mentionTrigger'
import {
  useSchemaMentionMenu,
  type MentionChoice,
  type MentionMenuController,
  type MentionSelf,
} from './useMentionMenu'

/** What the wiring needs from the editor component. */
export interface EditorMentionHost {
  /** The live view, or null before mount and after teardown. */
  view: () => EditorView | null
  /** The paragraph text before the cursor, per `SlashProvider.getContent`. */
  textBefore: (view: EditorView) => string | undefined
  /** Hides the slash provider's floating element. */
  hide: () => void
}

export interface EditorMention {
  menu: MentionMenuController
  /** The ProseMirror plugin to `.use()` on the editor. */
  plugin: ReturnType<typeof $prose>
  /** `SlashProvider.shouldShow`. */
  shouldShow: (view: EditorView) => boolean
  /**
   * Capture-phase keydown handler. Call it for every key: it first brings a
   * lagging menu up to date, then acts only if the menu is open.
   */
  onKeydown: (event: KeyboardEvent) => void
  /** A row was clicked. */
  onPick: (index: number) => void
  /** The pointer moved onto a row. */
  onHover: (index: number) => void
  dispose: () => void
}

export function useEditorMention(
  self: () => MentionSelf | null,
  host: EditorMentionHost
): EditorMention {
  const menu = useSchemaMentionMenu(self)

  /**
   * Whether the menu should be showing, and how wide its query is.
   *
   * Holds the Escape dismissal too: SlashProvider re-decides visibility on
   * every update, so closing the menu in the key handler alone did nothing —
   * the query still parsed and the menu came straight back.
   */
  const trigger = createMentionTrigger({
    isOpen: () => menu.state.open,
    close: () => menu.close(),
    setQuery: (query) => menu.setQuery(query),
  })

  const plugin = $prose(() =>
    mentionArmPlugin({
      scopeTypeFor: menu.scopeTypeFor,
      onBlur: () => {
        trigger.shouldShow(undefined, false)
        host.hide()
      },
    })
  )

  function shouldShow(view: EditorView): boolean {
    return trigger.shouldShow(host.textBefore(view), armedAt(view.state) !== null)
  }

  /**
   * Replaces the `@query` token with a reference to `item`.
   *
   * The title the menu just displayed is carried onto the node so the
   * reference reads correctly straight away, without waiting for a mentions
   * refresh that will not include a just-inserted ID.
   */
  function insertRef(view: EditorView, item: Entity, range: ArmedToken): void {
    const inserted = replaceMentionQueryWithRef(
      view,
      { id: item.id, title: entityDisplayTitle(item) || '', entityType: item.type ?? null },
      range
    )
    if (!inserted) return
    if (item.type) recordRecentEntity(item.id, item.type)
    menu.close()
  }

  /**
   * Acts on a row: an entity row inserts, a type row writes the scope.
   *
   * Shared by click and by Enter/Tab so the two paths cannot diverge on what a
   * row means. Choosing a type replaces what was typed to find it with
   * `@ticket:`, and the menu reads the new query from the document on the
   * next update (spec 4.1).
   */
  function commit(choice: MentionChoice): void {
    const view = host.view()
    if (!view) return
    // The choice answers `menu.state.query`. If the document has moved on
    // (a keystroke landed while Enter waited for the search), it answers a
    // question nobody is asking any more, so nothing is written.
    const range = armedToken(view.state)
    if (!range || range.query !== menu.state.query) return
    if (choice.kind === 'entity') insertRef(view, choice.entity, range)
    else replaceMentionQueryWithText(view, scopeText(choice.name), range)
    view.focus()
  }

  const handleMenuKey = createMentionKeydownHandler(menu, {
    commit,
    dismiss: (query) => {
      trigger.dismiss(query)
      menu.close()
      host.hide()
    },
  })

  /**
   * Brings the menu up to date with the document, then handles the key.
   *
   * SlashProvider reads the document on a debounced update, so a key pressed
   * straight after typing can arrive before the menu has seen the last
   * characters, or before it has opened at all. Enter would then fall through
   * and split the paragraph instead of waiting for the search (spec 6.7).
   */
  function onKeydown(event: KeyboardEvent): void {
    const view = host.view()
    if (view && armedAt(view.state) !== null) shouldShow(view)
    handleMenuKey(event)
  }

  function onPick(index: number): void {
    menu.setHighlight(index)
    const choice = menu.current()
    if (choice) commit(choice)
  }

  // A search that settled with nothing to show starts the no-match grace; the
  // trigger closes the menu a few characters later (spec 7.3). One that found
  // rows ends it.
  const stopNoMatchWatch = watch(
    () => [menu.state.open, menu.state.pending, menu.state.items.length, menu.state.query],
    () => {
      if (menu.settledEmpty()) trigger.markNoMatch(menu.state.query)
      else if (!menu.pending() && menu.state.items.length > 0) trigger.markMatch()
    }
  )

  return {
    menu,
    plugin,
    shouldShow,
    onKeydown,
    onPick,
    onHover: (index) => menu.setHighlight(index),
    dispose: () => {
      stopNoMatchWatch()
      menu.dispose()
    },
  }
}
