import { describe, it, expect, vi } from 'vitest'
import { createMentionKeydownHandler, type MentionKeymapHost } from './mentionKeymap'
import type { MentionChoice, MentionMenuController, MentionMenuState } from './useMentionMenu'

/** A controller stub exposing only what the keymap touches. */
function menuStub(
  state: Partial<MentionMenuState>,
  choice: MentionChoice | null = null,
  pending = false
) {
  const calls = {
    moveHighlight: vi.fn(),
    whenSettled: vi.fn(),
    close: vi.fn(),
  }
  const menuState: MentionMenuState = {
    open: true,
    query: '',
    items: [],
    // The shared snapshot's numeric index; the SPA menu resolves its own
    // highlight from `highlight` instead, so this is only here to satisfy the
    // inherited type.
    highlightedIndex: 0,
    typeItems: [],
    scopeType: null,
    typesFirst: true,
    starting: false,
    searches: false,
    highlight: null,
    loading: false,
    pending,
    errorMsg: '',
    ...state,
  }
  const menu = {
    state: menuState,
    current: () => choice,
    pending: () => pending,
    ...calls,
  } as unknown as MentionMenuController
  return { menu, calls }
}

function hostStub(): {
  host: MentionKeymapHost
  commit: ReturnType<typeof vi.fn>
  dismiss: ReturnType<typeof vi.fn>
} {
  const commit = vi.fn()
  const dismiss = vi.fn()
  return { host: { commit, dismiss }, commit, dismiss }
}

type KeyEventStub = KeyboardEvent & {
  preventDefault: ReturnType<typeof vi.fn>
  stopPropagation: ReturnType<typeof vi.fn>
}

function keyEvent(key: string): KeyEventStub {
  return { key, preventDefault: vi.fn(), stopPropagation: vi.fn() } as unknown as KeyEventStub
}

describe('createMentionKeydownHandler', () => {
  it.each(['Enter', 'ArrowDown', 'Escape'])('leaves %s to an IME that is composing', (key) => {
    const { menu, calls } = menuStub({}, { kind: 'type', name: 'ticket' })
    const host = { commit: vi.fn(), dismiss: vi.fn() }
    const event = new KeyboardEvent('keydown', { key, isComposing: true, cancelable: true })
    createMentionKeydownHandler(menu, host)(event)
    expect(event.defaultPrevented).toBe(false)
    expect(host.commit).not.toHaveBeenCalled()
    expect(host.dismiss).not.toHaveBeenCalled()
    expect(calls.moveHighlight).not.toHaveBeenCalled()
  })

  it('ignores every key while the menu is closed', () => {
    const { menu, calls } = menuStub({ open: false })
    const { host, commit } = hostStub()
    const handler = createMentionKeydownHandler(menu, host)
    for (const key of ['ArrowDown', 'Enter', 'Backspace', 'Escape']) {
      const ev = keyEvent(key)
      handler(ev)
      expect(ev.preventDefault).not.toHaveBeenCalled()
    }
    expect(calls.moveHighlight).not.toHaveBeenCalled()
    expect(commit).not.toHaveBeenCalled()
  })

  it('moves the highlight on the arrow keys', () => {
    const { menu, calls } = menuStub({})
    const { host } = hostStub()
    const handler = createMentionKeydownHandler(menu, host)

    const down = keyEvent('ArrowDown')
    handler(down)
    expect(down.preventDefault).toHaveBeenCalled()
    expect(calls.moveHighlight).toHaveBeenCalledWith(1)

    handler(keyEvent('ArrowUp'))
    expect(calls.moveHighlight).toHaveBeenLastCalledWith(-1)
  })

  it.each(['Enter', 'Tab'])('commits the highlighted choice on %s', (key) => {
    const choice: MentionChoice = { kind: 'type', name: 'ticket' }
    const { menu } = menuStub({}, choice)
    const { host, commit } = hostStub()
    const ev = keyEvent(key)
    createMentionKeydownHandler(menu, host)(ev)
    expect(ev.preventDefault).toHaveBeenCalled()
    expect(commit).toHaveBeenCalledWith(choice)
  })

  it('lets Enter through when there is nothing highlighted', () => {
    const { menu } = menuStub({}, null)
    const { host, commit } = hostStub()
    const ev = keyEvent('Enter')
    createMentionKeydownHandler(menu, host)(ev)
    // Not prevented: with no row to pick, Enter must stay a paragraph break.
    expect(ev.preventDefault).not.toHaveBeenCalled()
    expect(commit).not.toHaveBeenCalled()
  })

  it('dismisses on Escape, carrying the query so the menu stays shut', () => {
    const { menu } = menuStub({ query: 'tkt' })
    const { host, dismiss } = hostStub()
    const ev = keyEvent('Escape')
    createMentionKeydownHandler(menu, host)(ev)
    expect(ev.preventDefault).toHaveBeenCalled()
    // The global shortcut handler would otherwise blur the editor.
    expect(ev.stopPropagation).toHaveBeenCalled()
    expect(dismiss).toHaveBeenCalledWith('tkt')
  })

  it.each(['Enter', 'Tab'])('waits for a pending search on %s, then commits', (key) => {
    const { menu, calls } = menuStub({}, { kind: 'type', name: 'ticket' }, true)
    const { host, commit } = hostStub()
    const ev = keyEvent(key)
    createMentionKeydownHandler(menu, host)(ev)
    expect(ev.preventDefault).toHaveBeenCalled()
    expect(commit).not.toHaveBeenCalled()
    expect(calls.whenSettled).toHaveBeenCalledWith(commit)
  })

  it('leaves Backspace to the editor', () => {
    const { menu } = menuStub({ query: 'ticket:' })
    const { host } = hostStub()
    const ev = keyEvent('Backspace')
    createMentionKeydownHandler(menu, host)(ev)
    expect(ev.preventDefault).not.toHaveBeenCalled()
  })

  it('leaves an unrelated key alone', () => {
    const { menu, calls } = menuStub({})
    const { host, commit } = hostStub()
    const ev = keyEvent('a')
    createMentionKeydownHandler(menu, host)(ev)
    expect(ev.preventDefault).not.toHaveBeenCalled()
    expect(calls.moveHighlight).not.toHaveBeenCalled()
    expect(commit).not.toHaveBeenCalled()
  })
})
