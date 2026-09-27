/**
 * Keyboard handling for the `@` completion menu.
 *
 * Extracted from `MilkdownEditor.vue` so the menu's key semantics are testable
 * without mounting an editor, and so the component does not keep growing. The
 * editor still owns the document and the slash provider; this only decides
 * what a keystroke MEANS while the menu is open, and calls back for the parts
 * that touch ProseMirror.
 *
 * Backspace has no case here. A type scope is document text (`@ticket:`), so
 * deleting its `:` unscopes the query through ordinary editing.
 */
import type { MentionChoice, MentionMenuController } from './useMentionMenu'

/**
 * What the keymap needs from the editor.
 *
 * Declared here, at the call site, rather than exposing the editor's internals:
 * each member is one thing the handler cannot do for itself.
 */
export interface MentionKeymapHost {
  /** Acts on a row: insert an entity, or scope to a type. */
  commit: (choice: MentionChoice) => void
  /** Closes the menu and remembers the dismissed query so it stays shut. */
  dismiss: (query: string) => void
}

/**
 * Builds the capture-phase keydown handler.
 *
 * Must be bound in the CAPTURE phase so it runs before ProseMirror's own
 * keymap: otherwise Enter inserts a paragraph break and the arrow keys move the
 * cursor instead of the highlight.
 */
export function createMentionKeydownHandler(
  menu: MentionMenuController,
  host: MentionKeymapHost
): (event: KeyboardEvent) => void {
  return function onKeydownCapture(event: KeyboardEvent): void {
    // An IME composing text owns Enter and the arrows until it commits.
    if (!menu.state.open || event.isComposing) return
    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault()
        menu.moveHighlight(1)
        break
      case 'ArrowUp':
        event.preventDefault()
        menu.moveHighlight(-1)
        break
      case 'Enter':
      case 'Tab': {
        // A search still running answers the query on screen; the rows showing
        // may belong to an older one. Wait for it (spec 6.7). If it settles with
        // nothing to pick, the key is spent: it was prevented before the answer
        // was known, so a waiting Enter does not split the paragraph.
        if (menu.pending()) {
          event.preventDefault()
          menu.whenSettled(host.commit)
          break
        }
        // Nothing to pick: the key keeps its ordinary meaning, so Enter still
        // starts a new paragraph and `@xyz` stays as text (spec 7.2).
        const choice = menu.current()
        if (!choice) return
        event.preventDefault()
        host.commit(choice)
        break
      }
      case 'Escape':
        // Stopped as well as prevented: the app's global shortcut handler
        // blurs the editor on Escape, and a blur releases the `@`, so editing
        // the query could no longer bring the menu back (spec 8.1).
        event.preventDefault()
        event.stopPropagation()
        host.dismiss(menu.state.query)
        break
      default:
        break
    }
  }
}
