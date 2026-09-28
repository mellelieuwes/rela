import { ApiError, getErrorMessage, getScriptError } from '@/api/errors'
import type { ActionResponse } from '@/api/actions'
import { useUIStore } from '@/stores'
import { useScriptErrorStore } from '@/stores/scriptError'

/**
 * The feedback every single-run action surface gives: the sidebar, a
 * next-action offer and a detail-page action. One place, so the three cannot
 * drift in how they report a result or a failure.
 */
export function useActionFeedback() {
  const uiStore = useUIStore()
  const scriptErrorStore = useScriptErrorStore()

  /**
   * Toast the script's message in its own kind. Without a message, toast
   * `fallback` if given, unless the script redirected: a redirect IS the
   * feedback, and a toast that outlives the page it described is noise.
   */
  function reportResult(res: ActionResponse | null, fallback?: string) {
    if (res?.message) {
      // The enum is validated server-side against exactly these four names
      // (script/action.go), which are the store's methods.
      uiStore[res.message_type ?? 'success'](res.message)
    } else if (fallback && !res?.redirect) {
      uiStore.success(fallback)
    }
  }

  /**
   * A script error opens the dialog with file:line and correlation id;
   * anything else is a toast CARRYING the correlation id, which is the only
   * handle on the server log. `label`, when given, prefixes the toast.
   */
  function reportError(err: unknown, triggerEl: HTMLElement | null, label?: string) {
    const scriptErr = getScriptError(err)
    if (scriptErr) {
      scriptErrorStore.show(scriptErr, triggerEl)
      return
    }
    const corrID = err instanceof ApiError ? err.correlationId : undefined
    const detail = getErrorMessage(err, 'Action failed')
    const msg = label ? `${label}: ${detail}` : detail
    uiStore.error(corrID ? `${msg} (ref: ${corrID})` : msg)
  }

  return { reportResult, reportError }
}
