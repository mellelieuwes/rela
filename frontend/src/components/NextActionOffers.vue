<script setup lang="ts">
/**
 * The affordance row for a suggestion. Extracted so the page-level banner and
 * the status-bar popover render identical controls — a snooze that behaved
 * differently depending on where it was clicked would be a bug nobody thinks
 * to test for.
 *
 * The defer affordances (snooze durations, dismiss, mute) collapse into ONE
 * "Not now" menu. Flat, they read as three or four equal choices competing
 * with the thing the operator actually wants you to do; and only one of them
 * — mute — is about the source rather than this suggestion, which a row of
 * sibling buttons cannot express.
 */
import { computed, onMounted, onUnmounted, ref, useTemplateRef } from 'vue'
import { useRouter } from 'vue-router'
import { useNextAction } from '@/composables/useNextAction'
import { runAction } from '@/api/actions'
import { useConfirm } from '@/composables/useConfirm'
import { useActionFeedback } from '@/composables/useActionFeedback'
import type { NextActionOffer, NextActionPickOption } from '@/types'

const props = defineProps<{
  offers: NextActionOffer[]
  entityId?: string
  pickOptions?: Record<string, NextActionPickOption[]>
}>()

/**
 * Offers that ACT, paired with their index so a pick_one can find its live
 * options. The index is the key the server used, so filtering must not lose
 * it — hence the map-then-filter rather than a plain filter.
 *
 * `action` renders here too (BUG-G2BASF). It was previously filtered out, so
 * an offer the config layer validates — Kind() ranks it first in the union,
 * and validation rejects an unknown action id — rendered nothing at all.
 *
 * `set` is still absent, and deliberately: unlike `action` it has no endpoint
 * behind it, so a button would have nothing to call. Left to the ticket that
 * gives it one rather than faked client-side out of a PATCH, which would put
 * the interpolation rules in the wrong layer.
 */
const actEntries = computed(() =>
  props.offers
    .map((offer, index) => ({ offer, index }))
    .filter(({ offer }) => offer.navigate || offer.acknowledge || offer.pick_one || offer.action)
)

/** The resolved options for one offer, or [] when the server sent none. */
function optionsFor(index: number): NextActionPickOption[] {
  return props.pickOptions?.[String(index)] ?? []
}

const { busy, acting, respond, acknowledge, reload } = useNextAction()
const router = useRouter()
const { confirm } = useConfirm()
const { reportResult, reportError } = useActionFeedback()

// Whether this instance is still mounted. A redirect must not fire from a
// component the user has already navigated away from: `runAction` and
// `reload` are both awaited, and a slow script easily outlives the page.
let alive = true
onUnmounted(() => {
  alive = false
})

/**
 * Run an `action:` offer against the suggested entity.
 *
 * `confirm: true` is HONOURED rather than advisory — validation already
 * refuses it on anything but an action or set, so an operator who set it is
 * owed the prompt. An offer mutates the graph in one click with no undo.
 */
async function act(offer: NextActionOffer, triggerEl: HTMLElement | null) {
  // entityId is optional on the wire (a count-based source suggests without
  // one), and runAction omits the body entirely when it is absent — so the
  // action would run against NO entity, server-side and silently. Refuse
  // instead; the button is not rendered in that case either.
  if (!offer.action || !props.entityId || acting.value) return

  // Latched BEFORE the confirm await, not after it. The button's :disabled
  // only updates on re-render, so a double-click lands both handlers in the
  // same tick; latching after the dialog would open two of them and run the
  // mutation twice on two OKs.
  acting.value = true
  const label = offerLabel(offer, 'Do it')
  try {
    if (offer.confirm) {
      const ok = await confirm({
        title: `${label}?`,
        message: 'This runs immediately and cannot be undone.',
        confirmLabel: label,
      })
      if (!ok) return
    }
    // No entity type: the server reads it off the stored row and deliberately
    // ignores a caller-supplied one (actions_request.go, BUG-ZWTDH9).
    const res = await runAction(offer.action, props.entityId)
    reportResult(res, `${label}: done`)
    // The suggestion was resolved BY acting, so re-ask rather than leave a
    // banner recommending work that is now done.
    await reload()
    // Last, so the slot is already re-resolved when the destination renders,
    // and only if this instance still exists — see `alive`.
    if (res?.redirect && alive) router.push(res.redirect)
  } catch (err) {
    reportError(err, triggerEl, label)
  } finally {
    acting.value = false
  }
}

const deferOpen = ref(false)
const deferRef = useTemplateRef<HTMLElement>('deferRef')

/** Snooze durations the operator offered, flattened across offers. */
const snoozeDurations = computed(() => props.offers.flatMap((o) => o.snooze || []))
const hasDismiss = computed(() => props.offers.some((o) => o.dismiss))

function offerLabel(offer: NextActionOffer, fallback: string): string {
  return offer.label || fallback
}

function handleClickOutside(e: MouseEvent) {
  if (deferOpen.value && deferRef.value && !deferRef.value.contains(e.target as Node)) {
    deferOpen.value = false
  }
}

async function defer(kind: 'snooze' | 'dismiss' | 'mute', duration?: string) {
  deferOpen.value = false
  await respond(kind, duration)
}

onMounted(() => document.addEventListener('click', handleClickOutside))
onUnmounted(() => document.removeEventListener('click', handleClickOutside))
</script>

<template>
  <div class="na-offers rela-na-offers">
    <template v-for="{ offer, index } in actEntries" :key="index">
      <router-link
        v-if="offer.navigate"
        class="btn btn-sm btn-primary"
        :to="offer.navigate.replace('{id}', entityId || '')"
      >
        {{ offerLabel(offer, 'Open') }}
      </router-link>

      <button
        v-if="offer.acknowledge"
        class="btn btn-sm btn-secondary"
        :disabled="busy"
        @click="acknowledge()"
      >
        {{ offerLabel(offer, 'Nice') }}
      </button>

      <!-- action: run a configured Lua action against the suggested entity.
           Primary, like navigate — it is the thing being recommended. -->
      <button
        v-if="offer.action && entityId"
        class="btn btn-sm btn-primary"
        :disabled="busy || acting"
        @click="act(offer, $event.currentTarget as HTMLElement)"
      >
        {{ offerLabel(offer, 'Do it') }}
      </button>

      <!-- pick_one: one button per live option. Rendered only when the server
           resolved some — an empty list means the query matched nothing, and
           an empty option row would be worse than no affordance. -->
      <router-link
        v-for="opt in offer.pick_one ? optionsFor(index) : []"
        :key="opt.entity_id"
        class="btn btn-sm btn-secondary rela-na-pick"
        :to="`/entity/${opt.entity_id}`"
      >
        {{ opt.label }}
      </router-link>
    </template>

    <!--
      One "Not now" control for every way of NOT acting. Mute is always
      present, never operator-configured: without a one-click way to switch a
      source off, an annoying suggestion can only be escaped by complying with
      it — and the mute rate is the signal telling an operator which source to
      delete. It sits last, separated, because it is about the source rather
      than this suggestion.
    -->
    <div ref="deferRef" class="na-defer">
      <button
        type="button"
        class="na-defer__trigger rela-na-defer"
        :disabled="busy"
        aria-haspopup="menu"
        :aria-expanded="deferOpen"
        @click.stop="deferOpen = !deferOpen"
      >
        Not now
        <span class="na-defer__caret" aria-hidden="true">&#9662;</span>
      </button>

      <ul v-if="deferOpen" class="na-defer__menu" role="menu">
        <li v-for="d in snoozeDurations" :key="d" role="none">
          <button
            type="button"
            class="na-defer__item"
            role="menuitem"
            @click.stop="defer('snooze', d)"
          >
            Remind me in {{ d }}
          </button>
        </li>
        <li v-if="hasDismiss" role="none">
          <button
            type="button"
            class="na-defer__item"
            role="menuitem"
            @click.stop="defer('dismiss')"
          >
            Not this one
          </button>
        </li>
        <li role="none">
          <button
            type="button"
            class="na-defer__item na-defer__item--sep"
            role="menuitem"
            @click.stop="defer('mute')"
          >
            Stop suggesting this
          </button>
        </li>
      </ul>
    </div>
  </div>
</template>

<style scoped>
.na-offers {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}

/* The defer control is pushed to the trailing edge: reachable, never the
   obvious click. */
.na-defer {
  position: relative;
  display: inline-block;
  margin-left: auto;
}

/* Quieter than .btn-secondary on purpose — declining should not look like an
   equal-weight alternative to acting. */
.na-defer__trigger {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 6px 10px;
  border: 1px solid transparent;
  border-radius: var(--radius-md);
  background: none;
  color: var(--muted-text);
  font-size: var(--font-size-dense);
  cursor: pointer;
  transition: all 0.15s ease;
}

.na-defer__trigger:hover:not(:disabled) {
  border-color: var(--border-color);
  color: var(--text-color);
}

.na-defer__trigger:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}

.na-defer__caret {
  font-size: 11px;
  line-height: 1;
  opacity: 0.75;
}

/* Mirrors .status-menu (StatusControl) so the app has one dropdown look. */
.na-defer__menu {
  position: absolute;
  right: 0;
  z-index: 20;
  margin: 4px 0 0;
  padding: 4px;
  list-style: none;
  min-width: 190px;
  background: var(--input-bg);
  border: 1px solid var(--border-color);
  border-radius: var(--radius-md);
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.12);
}

.na-defer__item {
  display: flex;
  align-items: center;
  width: 100%;
  padding: 8px 10px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-color);
  cursor: pointer;
  font-size: var(--font-size-base);
  text-align: left;
  white-space: nowrap;
}

.na-defer__item:hover {
  background: var(--hover-bg);
}

/* Mute is a different kind of act — it governs the source, not this
   suggestion — so it is separated and muted rather than listed as a peer. */
.na-defer__item--sep {
  margin-top: 4px;
  border-top: 1px solid var(--border-color);
  padding-top: 8px;
  color: var(--muted-text);
}
</style>
